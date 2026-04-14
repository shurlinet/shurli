package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/shurlinet/shurli/internal/daemon"
	"github.com/shurlinet/shurli/internal/macaroon"
	"github.com/shurlinet/shurli/internal/termcolor"
	"github.com/shurlinet/shurli/pkg/sdk"
)

// writeJSON encodes v as indented JSON to w, wrapped in the standard
// {"status":"ok","data":...} envelope for machine consumption.
func writeJSON(w io.Writer, v any) error {
	envelope := struct {
		Status string `json:"status"`
		Data   any    `json:"data"`
	}{Status: "ok", Data: v}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(envelope)
}

// jsonError wraps an error so that run* wrappers emit JSON on stdout
// instead of plain text on stderr. The original error is still returned
// for non-zero exit code.
type jsonError struct {
	err    error
	stdout io.Writer
}

func (e *jsonError) Error() string { return e.err.Error() }

// jsonErr creates a jsonError that writes a JSON error envelope to stdout
// and still propagates the error for exit code 1.
func jsonErr(stdout io.Writer, err error) error {
	envelope := struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}{Status: "error", Error: err.Error()}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(envelope)
	return &jsonError{err: err, stdout: stdout}
}

// isJSONError returns true if the error was already emitted as JSON.
// Used by run* wrappers to avoid double-printing.
func isJSONError(err error) bool {
	_, ok := err.(*jsonError)
	return ok
}

// runWithJSON is the standard wrapper for commands that support --json.
// If the error was already emitted as JSON (via jsonErr), it exits without
// printing again. Otherwise it prints the error to stderr as usual.
func runWithJSON(err error) {
	if err == nil {
		return
	}
	if !isJSONError(err) {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
	}
	osExit(1)
}

// formatDelegation returns a human-readable delegation mode string.
func formatDelegation(maxDelegations int) string {
	switch {
	case maxDelegations == 0:
		return "disabled"
	case maxDelegations == -1:
		return "unlimited"
	default:
		return fmt.Sprintf("%d hops", maxDelegations)
	}
}

// formatEffectiveDur returns a human-readable duration (e.g. "4h" not "4h0m0s").
func formatEffectiveDur(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if d >= 24*time.Hour {
		days := h / 24
		h = h % 24
		if h > 0 {
			return fmt.Sprintf("%dd%dh", days, h)
		}
		return fmt.Sprintf("%dd", days)
	}
	if m > 0 {
		return fmt.Sprintf("%dh%dm", h, m)
	}
	return fmt.Sprintf("%dh", h)
}

// parseDelegateFlag converts the --delegate flag value to an int.
// Accepts: "0" (none), positive integers (limited hops), "unlimited" or "-1" (unlimited).
func parseDelegateFlag(s string) (int, error) {
	if s == "unlimited" {
		return -1, nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("invalid --delegate value %q: use a number or \"unlimited\"", s)
	}
	if v < -1 {
		return 0, fmt.Errorf("invalid --delegate value %d: minimum is -1 (unlimited)", v)
	}
	return v, nil
}

func runAuthGrant(args []string) {
	runWithJSON(doAuthGrant(args, os.Stdout))
}

func doAuthGrant(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("auth grant", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	duration := fs.String("duration", "1h", "grant duration (e.g. 1h, 7d, 30m)")
	services := fs.String("services", "", "comma-separated service names (empty = all)")
	service := fs.String("service", "", "single service name (alias for --services; takes effect when --services is empty)")
	permanent := fs.Bool("permanent", false, "grant permanent access (no expiry)")
	delegateStr := fs.String("delegate", "0", "delegation hops: 0=none (default), N=limited, unlimited")
	autoRefresh := fs.Bool("auto-refresh", false, "enable automatic token refresh before expiry")
	maxRefreshes := fs.Int("max-refreshes", 3, "max number of refreshes (requires --auto-refresh)")
	bandwidth := fs.String("bandwidth", "", "also set bandwidth_budget on the peer (e.g. 500MB, 1GB, unlimited)")
	budget := fs.String("budget", "", "per-peer data budget — same semantics as --bandwidth (e.g. 20GB); wired into bandwidth_budget peer attribute and Grant.DataBudget")
	transport := fs.String("transport", "", "transport caveat: comma-separated \"lan,direct,relay\" (empty = no caveat, any transport allowed)")
	jsonFlag := fs.Bool("json", false, "output as JSON")
	if err := fs.Parse(reorderArgs(args, nil)); err != nil {
		return err
	}

	// errOut wraps errors as JSON when --json is set.
	errOut := func(err error) error {
		if *jsonFlag {
			return jsonErr(stdout, err)
		}
		return err
	}

	if fs.NArg() != 1 {
		return errOut(fmt.Errorf("usage: shurli auth grant <peer> [--duration 1h] [--service <svc>|--services svc1,svc2,...] [--permanent] [--delegate N|unlimited] [--auto-refresh] [--max-refreshes N] [--transport lan,direct,relay] [--budget 20GB|--bandwidth 20GB]"))
	}

	normServices, budgetStr, err := normalizeAuthGrantFlags(*service, *services, *budget, *bandwidth, *transport)
	if err != nil {
		return errOut(err)
	}
	*services = normServices

	delegateVal, err := parseDelegateFlag(*delegateStr)
	if err != nil {
		return errOut(err)
	}

	if *autoRefresh && *permanent {
		return errOut(fmt.Errorf("--auto-refresh and --permanent are mutually exclusive (permanent grants never expire)"))
	}

	peerName := fs.Arg(0)

	// Permanent grants require confirmation (E4 mitigation).
	// Skip prompt in JSON mode - agents must not use --permanent without --json understanding the risk.
	if *permanent && !*jsonFlag {
		fmt.Fprint(stdout, "Permanent grants cannot be auto-expired. Are you sure? [y/N] ")
		var confirm string
		fmt.Scanln(&confirm)
		if confirm != "y" && confirm != "Y" {
			fmt.Fprintln(stdout, "Cancelled.")
			return nil
		}
	}

	var svcList []string
	if *services != "" {
		for _, s := range strings.Split(*services, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				svcList = append(svcList, s)
			}
		}
	}

	client, err := daemon.NewClient(daemonSocketPath(), daemonCookiePath())
	if err != nil {
		return errOut(err)
	}

	req := daemon.GrantRequest{
		Peer:           peerName,
		Duration:       *duration,
		Services:       svcList,
		Permanent:      *permanent,
		MaxDelegations: delegateVal,
		AutoRefresh:    *autoRefresh,
		MaxRefreshes:   *maxRefreshes,
		Transports:     *transport,
		Budget:         budgetStr,
	}

	info, err := client.GrantCreate(req)
	if err != nil {
		return errOut(err)
	}

	// Auto-sync budget into the bandwidth_budget peer attribute so the
	// existing filetransfer bandwidth tracker picks it up immediately.
	// Same path as legacy --bandwidth, but driven by a single normalized
	// value so --budget and --bandwidth behave identically.
	if budgetStr != "" {
		if setErr := doAuthSetAttr([]string{info.PeerID, "bandwidth_budget", budgetStr}, io.Discard); setErr != nil {
			if *jsonFlag {
				slog.Warn("grant created but failed to set bandwidth_budget", "peer", info.PeerID, "error", setErr)
			} else {
				fmt.Fprintf(stdout, "Warning: grant created but failed to set bandwidth_budget: %v\n", setErr)
			}
		}
	}

	if *jsonFlag {
		return writeJSON(stdout, info)
	}

	svcDesc := "all services"
	if len(info.Services) > 0 {
		svcDesc = strings.Join(info.Services, ", ")
	}
	termcolor.Green("Granted relay data access to %s", info.Peer)
	fmt.Fprintf(stdout, "  Services:   %s\n", svcDesc)
	if info.Permanent {
		fmt.Fprintln(stdout, "  Duration:   permanent")
	} else {
		fmt.Fprintf(stdout, "  Expires:    %s (%s remaining)\n", info.ExpiresAt, info.Remaining)
	}
	fmt.Fprintf(stdout, "  Delegation: %s\n", formatDelegation(info.MaxDelegations))
	if info.Transports != "" {
		fmt.Fprintf(stdout, "  Transport:  %s\n", info.Transports)
	}
	if budgetStr != "" {
		fmt.Fprintf(stdout, "  Bandwidth:  %s per peer\n", budgetStr)
	}
	if info.DataBudgetHR != "" {
		fmt.Fprintf(stdout, "  Data budget: %s (per-grant)\n", info.DataBudgetHR)
	}
	if *autoRefresh {
		effectiveDur, _ := time.ParseDuration(*duration)
		effectiveMax := effectiveDur * time.Duration(*maxRefreshes+1)
		fmt.Fprintf(stdout, "  Refresh:    auto-refresh: %d refreshes, %s effective max duration\n", *maxRefreshes, formatEffectiveDur(effectiveMax))
	}
	return nil
}

func runAuthGrants(args []string) {
	runWithJSON(doAuthGrants(args, os.Stdout))
}

func doAuthGrants(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("auth grants", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	jsonFlag := fs.Bool("json", false, "output as JSON")
	if err := fs.Parse(reorderArgs(args, nil)); err != nil {
		return err
	}

	errOut := func(err error) error {
		if *jsonFlag {
			return jsonErr(stdout, err)
		}
		return err
	}

	client, err := daemon.NewClient(daemonSocketPath(), daemonCookiePath())
	if err != nil {
		return errOut(err)
	}

	resp, err := client.GrantList()
	if err != nil {
		return errOut(err)
	}

	if *jsonFlag {
		return writeJSON(stdout, resp)
	}

	if len(resp.Grants) == 0 {
		fmt.Fprintln(stdout, "No active relay data access grants.")
		fmt.Fprintln(stdout, "\nTip: use 'shurli auth grant <peer> --duration 1h' to grant relay data access.")
		return nil
	}

	fmt.Fprintf(stdout, "Active relay data access grants (%d):\n\n", len(resp.Grants))
	for i, g := range resp.Grants {
		svc := "all"
		if len(g.Services) > 0 {
			svc = strings.Join(g.Services, ",")
		}
		dur := g.Remaining
		if g.Permanent {
			dur = "permanent"
		}
		delStr := ""
		if g.MaxDelegations != 0 {
			delStr = "  delegate:" + formatDelegation(g.MaxDelegations)
		}
		refreshStr := ""
		if g.AutoRefresh {
			refreshStr = fmt.Sprintf("  refresh:%d/%d", g.RefreshesUsed, g.MaxRefreshes)
		}
		transportStr := ""
		if g.Transports != "" {
			transportStr = "  transport:" + g.Transports
		}
		budgetStr := ""
		if g.DataBudgetHR != "" {
			budgetStr = "  budget:" + g.DataBudgetHR
		}
		fmt.Fprintf(stdout, "  %d. %s  [%s]  %s%s%s%s%s\n", i+1, g.Peer, svc, dur, delStr, refreshStr, transportStr, budgetStr)
		termcolor.Faint("     %s\n", g.PeerID)
	}
	return nil
}

func runAuthRevoke(args []string) {
	runWithJSON(doAuthRevoke(args, os.Stdout))
}

func doAuthRevoke(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("auth revoke", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	jsonFlag := fs.Bool("json", false, "output as JSON")
	if err := fs.Parse(reorderArgs(args, nil)); err != nil {
		return err
	}

	errOut := func(err error) error {
		if *jsonFlag {
			return jsonErr(stdout, err)
		}
		return err
	}

	if fs.NArg() != 1 {
		return errOut(fmt.Errorf("usage: shurli auth revoke <peer>"))
	}

	peerName := fs.Arg(0)

	client, err := daemon.NewClient(daemonSocketPath(), daemonCookiePath())
	if err != nil {
		return errOut(err)
	}

	if err := client.GrantRevoke(peerName); err != nil {
		return errOut(err)
	}

	if *jsonFlag {
		return writeJSON(stdout, map[string]string{"peer": peerName, "action": "revoked"})
	}

	termcolor.Green("Revoked relay data access for %s", peerName)
	fmt.Fprintln(stdout, "  All connections to this peer have been closed.")
	return nil
}

func runAuthDelegate(args []string) {
	runWithJSON(doAuthDelegate(args, os.Stdout))
}

func doAuthDelegate(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("auth delegate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	to := fs.String("to", "", "target peer to delegate to (required)")
	duration := fs.String("duration", "", "optional shorter duration (e.g. 30m)")
	services := fs.String("services", "", "optional comma-separated service names")
	delegateStr := fs.String("delegate", "0", "further delegation hops for target (0=none, N, unlimited)")
	transport := fs.String("transport", "", "optional narrower transport caveat (lan,direct,relay)")
	jsonFlag := fs.Bool("json", false, "output as JSON")
	if err := fs.Parse(reorderArgs(args, nil)); err != nil {
		return err
	}

	errOut := func(err error) error {
		if *jsonFlag {
			return jsonErr(stdout, err)
		}
		return err
	}

	if fs.NArg() != 1 {
		return errOut(fmt.Errorf("usage: shurli auth delegate <peer> --to <target> [--duration 30m] [--services file-browse] [--delegate N|unlimited] [--transport lan,direct,relay]"))
	}
	if *to == "" {
		return errOut(fmt.Errorf("--to is required (target peer for delegation)"))
	}

	// Early validation of optional transport narrowing.
	if *transport != "" {
		if _, err := macaroon.ParseTransportMask(*transport); err != nil {
			return errOut(fmt.Errorf("invalid --transport value %q: %w", *transport, err))
		}
	}

	delegateVal, err := parseDelegateFlag(*delegateStr)
	if err != nil {
		return errOut(err)
	}

	peerName := fs.Arg(0)

	var svcList []string
	if *services != "" {
		for _, s := range strings.Split(*services, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				svcList = append(svcList, s)
			}
		}
	}

	client, err := daemon.NewClient(daemonSocketPath(), daemonCookiePath())
	if err != nil {
		return errOut(err)
	}

	req := daemon.GrantDelegateRequest{
		Peer:           peerName,
		To:             *to,
		Duration:       *duration,
		Services:       svcList,
		MaxDelegations: delegateVal,
		Transports:     *transport,
	}

	result, err := client.GrantDelegate(req)
	if err != nil {
		return errOut(err)
	}

	if *jsonFlag {
		return writeJSON(stdout, result)
	}

	status := result["status"]
	target := result["target"]
	if status == "queued" {
		termcolor.Green("Delegated grant to %s (queued for delivery - peer offline)", target)
	} else {
		termcolor.Green("Delegated grant to %s (delivered)", target)
	}
	fmt.Fprintf(stdout, "  From: %s's grant\n", peerName)
	if *duration != "" {
		fmt.Fprintf(stdout, "  Duration: %s\n", *duration)
	}
	if len(svcList) > 0 {
		fmt.Fprintf(stdout, "  Services: %s\n", strings.Join(svcList, ", "))
	}
	fmt.Fprintf(stdout, "  Delegation: %s\n", formatDelegation(delegateVal))
	return nil
}

// normalizeAuthGrantFlags validates and reconciles the transport/service/
// budget flags accepted by `shurli auth grant`. It is pulled out as a pure
// helper so unit tests can cover the mutual-exclusion and validation logic
// without a daemon round-trip.
//
// Returns the canonical services string (possibly promoted from --service),
// the canonical budget string (possibly promoted from --bandwidth), or a
// first error encountered. Empty inputs produce empty outputs — the caller
// treats empty as "no caveat / use defaults".
func normalizeAuthGrantFlags(serviceFlag, servicesFlag, budgetFlag, bandwidthFlag, transportFlag string) (services string, budget string, err error) {
	// Transport mask pre-validation — fail fast, no daemon round-trip.
	if transportFlag != "" {
		if _, terr := macaroon.ParseTransportMask(transportFlag); terr != nil {
			return "", "", fmt.Errorf("invalid --transport value %q: %w", transportFlag, terr)
		}
	}

	// --service is a convenience alias for --services. It only kicks in when
	// --services is empty. If both are set they must name the same value, else
	// reject to prevent a silent drop.
	services = servicesFlag
	if services == "" && serviceFlag != "" {
		services = serviceFlag
	} else if services != "" && serviceFlag != "" && services != serviceFlag {
		return "", "", fmt.Errorf("--service and --services are mutually exclusive (use one)")
	}

	// --budget and --bandwidth both set the bandwidth_budget peer attribute.
	// --budget additionally populates Grant.DataBudget (forensic + future).
	// Mutually exclusive unless they name the exact same value.
	budget = bandwidthFlag
	if budgetFlag != "" {
		if budget != "" && budget != budgetFlag {
			return "", "", fmt.Errorf("--budget and --bandwidth are mutually exclusive (use one)")
		}
		budget = budgetFlag
	}
	if budget != "" {
		if _, berr := sdk.ParseByteSize(budget); berr != nil {
			return "", "", fmt.Errorf("invalid budget value %q: %w", budget, berr)
		}
	}
	return services, budget, nil
}

func runAuthPouch(args []string) {
	runWithJSON(doAuthPouch(args, os.Stdout))
}

func doAuthPouch(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("auth pouch", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	jsonFlag := fs.Bool("json", false, "output as JSON")
	if err := fs.Parse(reorderArgs(args, nil)); err != nil {
		return err
	}

	errOut := func(err error) error {
		if *jsonFlag {
			return jsonErr(stdout, err)
		}
		return err
	}

	client, err := daemon.NewClient(daemonSocketPath(), daemonCookiePath())
	if err != nil {
		return errOut(err)
	}

	resp, err := client.PouchList()
	if err != nil {
		return errOut(err)
	}

	if *jsonFlag {
		return writeJSON(stdout, resp)
	}

	if len(resp.Entries) == 0 {
		fmt.Fprintln(stdout, "No grant tokens received from other nodes.")
		fmt.Fprintln(stdout, "\nGrant tokens are delivered automatically when a node grants you relay data access.")
		return nil
	}

	fmt.Fprintf(stdout, "Received grant tokens (%d):\n\n", len(resp.Entries))
	for i, e := range resp.Entries {
		svc := "all services"
		if len(e.Services) > 0 {
			svc = strings.Join(e.Services, ", ")
		}
		dur := e.Remaining
		if e.Permanent {
			dur = "permanent"
		}
		transportStr := ""
		if e.Transports != "" {
			transportStr = "  transport:" + e.Transports
		}
		fmt.Fprintf(stdout, "  %d. %s  [%s]  %s%s\n", i+1, e.Issuer, svc, dur, transportStr)
		termcolor.Faint("     %s\n", e.IssuerID)
	}
	return nil
}

func runAuthExtend(args []string) {
	runWithJSON(doAuthExtend(args, os.Stdout))
}

func doAuthExtend(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("auth extend", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	duration := fs.String("duration", "", "new duration from now (e.g. 2h, 1d)")
	maxRefreshes := fs.Int("max-refreshes", -1, "update max refresh count (-1 = no change)")
	jsonFlag := fs.Bool("json", false, "output as JSON")
	if err := fs.Parse(reorderArgs(args, nil)); err != nil {
		return err
	}

	errOut := func(err error) error {
		if *jsonFlag {
			return jsonErr(stdout, err)
		}
		return err
	}

	if fs.NArg() != 1 {
		return errOut(fmt.Errorf("usage: shurli auth extend <peer> --duration 2h [--max-refreshes N]"))
	}
	if *duration == "" && *maxRefreshes < 0 {
		return errOut(fmt.Errorf("--duration or --max-refreshes is required"))
	}

	peerName := fs.Arg(0)

	client, err := daemon.NewClient(daemonSocketPath(), daemonCookiePath())
	if err != nil {
		return errOut(err)
	}

	req := daemon.GrantExtendRequest{
		Peer:     peerName,
		Duration: *duration,
	}
	if *maxRefreshes >= 0 {
		v := *maxRefreshes
		req.MaxRefreshes = &v
	}

	if err := client.GrantExtendFull(req); err != nil {
		return errOut(err)
	}

	if *jsonFlag {
		result := map[string]string{"peer": peerName, "action": "extended"}
		if *duration != "" {
			result["duration"] = *duration
		}
		return writeJSON(stdout, result)
	}

	termcolor.Green("Extended relay data access for %s", peerName)
	if *duration != "" {
		fmt.Fprintf(stdout, "  Duration: %s from now\n", *duration)
	}
	if *maxRefreshes >= 0 {
		fmt.Fprintf(stdout, "  Max refreshes: %d\n", *maxRefreshes)
	}
	return nil
}
