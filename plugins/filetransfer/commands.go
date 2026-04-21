package filetransfer

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tc "github.com/shurlinet/shurli/internal/termcolor"
	"github.com/shurlinet/shurli/pkg/sdk"
	"github.com/shurlinet/shurli/pkg/plugin"
)

// fatal prints an error and exits.
func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "Error: "+format+"\n", args...)
	osExit(1)
}

// tryClient creates a daemon client, returning nil if the daemon is not running.
func tryClient() *daemonClient {
	c, err := newDaemonClient()
	if err != nil {
		return nil
	}
	return c
}

// requireClient creates a daemon client, exiting if the daemon is not running.
func requireClient() *daemonClient {
	c, err := newDaemonClient()
	if err != nil {
		fmt.Println("Daemon not running. Start it with:")
		fmt.Println("  shurli daemon")
		osExit(1)
	}
	return c
}

// RegisterCLI registers file transfer CLI commands with the plugin command registry.
// Called from plugins.RegisterCLI() at startup. NO init() - dynamic registration.
func RegisterCLI() {
	for _, cmd := range cliCommandList() {
		plugin.RegisterCLICommand(cmd)
	}
}

func cliCommandList() []plugin.CLICommandEntry {
	return []plugin.CLICommandEntry{
		{
			Name: "send", PluginName: "filetransfer",
			Description: "Send a file to a peer",
			Usage:       "shurli send <file> <peer> [--follow] [--json]",
			Run:         runSend,
			Flags: []plugin.CLIFlagEntry{
				{Long: "follow", Type: "bool", Description: "Follow transfer progress"},
				{Long: "json", Type: "bool", Description: "Output as JSON"},
				{Long: "no-compress", Type: "bool", Description: "Disable zstd compression"},
				{Long: "streams", Type: "int", Description: "Parallel stream count", RequiresArg: true},
				{Long: "priority", Type: "enum", Description: "Queue priority", Enum: []string{"low", "normal", "high"}, RequiresArg: true},
				{Long: "rate", Type: "string", Description: "Send rate limit (e.g. 100M, 500M, 0=unlimited)", RequiresArg: true},
				{Long: "quiet", Type: "bool", Description: "Show only a single progress bar"},
				{Long: "silent", Type: "bool", Description: "No progress output"},
			},
		},
		{
			Name: "download", PluginName: "filetransfer",
			Description: "Download from peer's shared files",
			Usage:       "shurli download <peer>:<path> [--dest dir] [--json]",
			Run:         runDownload,
			Flags: []plugin.CLIFlagEntry{
				{Long: "json", Type: "bool", Description: "Output as JSON"},
				{Long: "dest", Type: "directory", Description: "Local directory to save into", RequiresArg: true},
				{Long: "follow", Type: "bool", Description: "Follow transfer progress"},
				{Long: "quiet", Type: "bool", Description: "Show only a single progress bar"},
				{Long: "silent", Type: "bool", Description: "No progress output"},
				{Long: "multi-peer", Type: "bool", Description: "Enable multi-peer download (requires --peers)"},
				{Long: "peers", Type: "string", Description: "Extra peer names/IDs for multi-peer download (comma-separated, implies --multi-peer)", RequiresArg: true},
			},
		},
		{
			Name: "browse", PluginName: "filetransfer",
			Description: "Browse peer's shared files",
			Usage:       "shurli browse <peer> [path] [--json]",
			Run:         runBrowse,
			Flags: []plugin.CLIFlagEntry{
				{Long: "json", Type: "bool", Description: "Output as JSON"},
				{Long: "path", Type: "string", Description: "Browse within a shared directory", RequiresArg: true},
			},
		},
		{
			Name: "share", PluginName: "filetransfer",
			Description: "Manage shared files",
			Usage:       "shurli share <add|remove|list|deny> ...",
			Run:         runShare,
			Subcommands: []plugin.CLISubcommand{
				{Name: "add", Description: "Share a file or directory", Flags: []plugin.CLIFlagEntry{
					{Long: "to", Type: "string", Description: "Share with a single peer", RequiresArg: true},
					{Long: "peers", Type: "string", Description: "Share with multiple peers", RequiresArg: true},
					{Long: "persist", Type: "bool", Description: "Survive daemon restart"},
					{Long: "json", Type: "bool", Description: "Output as JSON"},
				}},
				{Name: "remove", Description: "Stop sharing a path", Flags: []plugin.CLIFlagEntry{
					{Long: "json", Type: "bool", Description: "Output as JSON"},
				}},
				{Name: "list", Description: "List shared paths", Flags: []plugin.CLIFlagEntry{
					{Long: "json", Type: "bool", Description: "Output as JSON"},
				}},
				{Name: "deny", Description: "Remove a peer from a share", Flags: []plugin.CLIFlagEntry{
					{Long: "json", Type: "bool", Description: "Output as JSON"},
				}},
			},
		},
		{
			Name: "transfers", PluginName: "filetransfer",
			Description: "List active transfers",
			Usage:       "shurli transfers [--watch] [--json]",
			Run:         runTransfers,
			Flags: []plugin.CLIFlagEntry{
				{Long: "json", Type: "bool", Description: "Output as JSON"},
				{Long: "watch", Type: "bool", Description: "Live feed (refreshes every 2s)"},
				{Long: "history", Type: "bool", Description: "Show transfer event history"},
				{Long: "max", Type: "int", Description: "Max events with --history", RequiresArg: true},
			},
		},
		{
			Name: "accept", PluginName: "filetransfer",
			Description: "Accept a pending transfer",
			Usage:       "shurli accept <id|--all> [--dest dir] [--json]",
			Run:         runAccept,
			Flags: []plugin.CLIFlagEntry{
				{Long: "all", Type: "bool", Description: "Accept all pending transfers"},
				{Long: "dest", Type: "directory", Description: "Save to a specific directory", RequiresArg: true},
				{Long: "json", Type: "bool", Description: "Output as JSON"},
			},
		},
		{
			Name: "reject", PluginName: "filetransfer",
			Description: "Reject a pending transfer",
			Usage:       "shurli reject <id|--all> [--reason msg] [--json]",
			Run:         runReject,
			Flags: []plugin.CLIFlagEntry{
				{Long: "all", Type: "bool", Description: "Reject all pending transfers"},
				{Long: "reason", Type: "enum", Description: "Reject reason", Enum: []string{"space", "busy", "size"}, RequiresArg: true},
				{Long: "json", Type: "bool", Description: "Output as JSON"},
			},
		},
		{
			Name: "cancel", PluginName: "filetransfer",
			Description: "Cancel a transfer",
			Usage:       "shurli cancel <id> [--json]",
			Run:         runCancel,
			Flags: []plugin.CLIFlagEntry{
				{Long: "json", Type: "bool", Description: "Output as JSON"},
			},
		},
		{
			Name: "clean", PluginName: "filetransfer",
			Description: "Clean temp files",
			Usage:       "shurli clean [--json]",
			Run:         runClean,
			Flags: []plugin.CLIFlagEntry{
				{Long: "json", Type: "bool", Description: "Output as JSON"},
			},
		},
	}
}

// reorderFlags moves flags before positional arguments so Go's flag package works
// regardless of flag position. Copied from cmd/shurli/flagorder.go (72 lines, stable).
func reorderFlags(fs *flag.FlagSet, args []string) []string {
	if len(args) == 0 {
		return args
	}

	boolFlags := make(map[string]bool)
	allFlags := make(map[string]bool)
	fs.VisitAll(func(f *flag.Flag) {
		allFlags[f.Name] = true
		type boolFlagger interface {
			IsBoolFlag() bool
		}
		if bf, ok := f.Value.(boolFlagger); ok && bf.IsBoolFlag() {
			boolFlags[f.Name] = true
		}
	})

	var flags, positional []string

	for i := 0; i < len(args); i++ {
		arg := args[i]

		if arg == "--" {
			positional = append(positional, args[i:]...)
			break
		}

		if !strings.HasPrefix(arg, "-") {
			positional = append(positional, arg)
			continue
		}

		flags = append(flags, arg)
		name := strings.TrimLeft(arg, "-")
		if idx := strings.IndexByte(name, '='); idx >= 0 {
			continue
		}
		if boolFlags[name] {
			continue
		}
		if allFlags[name] && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}

	return append(flags, positional...)
}

// --- CLI command implementations ---

func runSend(args []string) {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	jsonFlag := fs.Bool("json", false, "output as JSON")
	followFlag := fs.Bool("follow", false, "follow transfer progress inline")
	noCompressFlag := fs.Bool("no-compress", false, "disable zstd compression")
	streamsFlag := fs.Int("streams", 0, "parallel stream count (0 = auto)")
	priorityFlag := fs.String("priority", "normal", "queue priority: low, normal, high")
	rateFlag := fs.String("rate", "", "send rate limit bytes/sec (e.g. 100M, 500M, 0=unlimited)")
	quietFlag := fs.Bool("quiet", false, "show only a single progress bar")
	silentFlag := fs.Bool("silent", false, "no progress output")
	fs.Parse(reorderFlags(fs, args))

	remaining := fs.Args()
	if len(remaining) < 2 {
		fmt.Println("Usage: shurli send <file|dir> <peer> [--follow] [--no-compress] [--streams N] [--json]")
		fmt.Println()
		fmt.Println("Send a file or directory to a peer.")
		osExit(1)
	}

	filePath := remaining[0]
	peerArg := remaining[1]

	if peerArg == "--to" {
		if len(remaining) < 3 {
			fatal("Missing peer after --to")
		}
		peerArg = remaining[2]
	}

	absPath, err := filepath.Abs(filePath)
	if err != nil {
		fatal("Invalid path: %v", err)
	}

	info, err := os.Stat(absPath)
	if err != nil {
		fatal("Cannot access file: %v", err)
	}

	client := tryClient()
	if client == nil {
		fmt.Println("Daemon not running. Start it with:")
		fmt.Println("  shurli daemon")
		osExit(1)
	}

	if info.IsDir() {
		if !*jsonFlag {
			tc.Wfaint(os.Stdout, "Sending directory %s to %s...\n", filepath.Base(absPath), peerArg)
		}
	} else {
		if !*jsonFlag {
			tc.Wfaint(os.Stdout, "Sending %s (%s) to %s...\n", filepath.Base(absPath), humanSize(info.Size()), peerArg)
		}
	}

	resp, err := client.Send(absPath, peerArg, *noCompressFlag, *streamsFlag, *priorityFlag, *rateFlag)
	if err != nil {
		fatal("Send failed: %s", sdk.HumanizeError(err.Error()))
	}

	if *jsonFlag {
		enc := json.NewEncoder(os.Stdout)
		if *followFlag {
			enc.Encode(map[string]any{
				"event":       "started",
				"transfer_id": resp.TransferID,
				"filename":    resp.Filename,
				"size":        resp.Size,
				"peer_id":     resp.PeerID,
			})
			pollTransferJSON(client, resp.TransferID)
		} else {
			enc.SetIndent("", "  ")
			enc.Encode(resp)
		}
		return
	}

	tc.Wgreen(os.Stdout, "Transfer started")
	fmt.Printf(" [%s]\n", resp.TransferID)

	if !*followFlag || *silentFlag {
		if !*silentFlag {
			tc.Wfaint(os.Stdout, "Transfer continues in daemon. Check: shurli transfers\n")
		}
		return
	}

	pollTransfer(client, resp.TransferID, *quietFlag)
}

func runDownload(args []string) {
	fs := flag.NewFlagSet("download", flag.ExitOnError)
	jsonFlag := fs.Bool("json", false, "output as JSON")
	destFlag := fs.String("dest", "", "local directory to save into")
	followFlag := fs.Bool("follow", false, "follow transfer progress inline")
	quietFlag := fs.Bool("quiet", false, "show only a single progress bar")
	silentFlag := fs.Bool("silent", false, "no progress output")
	multiPeerFlag := fs.Bool("multi-peer", false, "enable multi-peer download (requires --peers)")
	extraPeersFlag := fs.String("peers", "", "extra peer names/IDs for multi-peer (comma-separated, implies --multi-peer)")
	fs.Parse(reorderFlags(fs, args))

	remaining := fs.Args()
	if len(remaining) < 1 {
		fmt.Println("Usage: shurli download <peer>:<shareID/filename> [--dest /local/dir] [--follow] [--json]")
		osExit(1)
	}

	arg := remaining[0]
	if len(remaining) > 1 {
		// Extra args may be a path with spaces that wasn't quoted.
		next := remaining[1]
		if !strings.HasPrefix(next, "-") {
			fmt.Fprintf(os.Stderr, "Warning: path appears to contain spaces. Use quotes:\n")
			fmt.Fprintf(os.Stderr, "  shurli download '%s'\n", strings.Join(remaining, " "))
			osExit(1)
		}
	}
	peerArg, remotePath := parsePeerPath(arg)
	if peerArg == "" || remotePath == "" {
		fatal("Invalid format. Use: <peer>:<shareID/filename>")
	}

	client := tryClient()
	if client == nil {
		fmt.Println("Daemon not running. Start it with: shurli daemon")
		osExit(1)
	}

	if !*jsonFlag && !*silentFlag {
		tc.Wfaint(os.Stdout, "Downloading %s from %s...\n", remotePath, peerArg)
	}

	var extraPeers []string
	if *extraPeersFlag != "" {
		for _, p := range strings.Split(*extraPeersFlag, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				extraPeers = append(extraPeers, p)
			}
		}
	}

	// IF16-2: --peers implies --multi-peer (no flag redundancy needed).
	if len(extraPeers) > 0 {
		*multiPeerFlag = true
	}

	// IF16-3: --multi-peer without --peers prints a hint.
	if *multiPeerFlag && len(extraPeers) == 0 {
		if !*jsonFlag && !*silentFlag {
			tc.Wfaint(os.Stdout, "Note: --multi-peer requires --peers to specify additional sources. Using single-peer.\n")
		}
	}

	resp, err := client.Download(peerArg, remotePath, *destFlag, *multiPeerFlag, extraPeers)
	if err != nil {
		fatal("Download failed: %s", sdk.HumanizeError(err.Error()))
	}

	if *jsonFlag {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(resp)
		return
	}

	tc.Wgreen(os.Stdout, "Download started")
	fmt.Printf(" [%s] %s (%s)\n", resp.TransferID, SanitizeDisplayName(resp.FileName), humanSize(resp.FileSize))

	if !*followFlag || *silentFlag {
		if !*silentFlag {
			tc.Wfaint(os.Stdout, "Transfer continues in daemon. Check: shurli transfers\n")
		}
		return
	}

	pollTransfer(client, resp.TransferID, *quietFlag)
}

// parsePeerPath splits "peer:path" on the first colon.
func parsePeerPath(s string) (string, string) {
	idx := strings.Index(s, ":")
	if idx < 2 {
		return "", ""
	}
	return s[:idx], s[idx+1:]
}

func runBrowse(args []string) {
	fs := flag.NewFlagSet("browse", flag.ExitOnError)
	jsonFlag := fs.Bool("json", false, "output as JSON")
	pathFlag := fs.String("path", "", "browse within a shared directory")
	fs.Parse(reorderFlags(fs, args))

	remaining := fs.Args()
	if len(remaining) < 1 {
		fmt.Println("Usage: shurli browse <peer> [<path>] [--json]")
		osExit(1)
	}
	if len(remaining) > 2 {
		fatal("Too many arguments.")
	}

	peerArg := remaining[0]
	browsePath := *pathFlag
	if len(remaining) > 1 {
		if browsePath != "" {
			fatal("Specify path as positional argument or --path flag, not both")
		}
		browsePath = remaining[1]
	}

	client := tryClient()
	if client == nil {
		fmt.Println("Daemon not running. Start it with: shurli daemon")
		osExit(1)
	}

	if *jsonFlag {
		resp, err := client.Browse(peerArg, browsePath)
		if err != nil {
			fatal("Browse failed: %v", err)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(resp)
		return
	}

	text, err := client.BrowseText(peerArg, browsePath)
	if err != nil {
		fatal("Browse failed: %v", err)
	}

	if text == "" {
		tc.Wfaint(os.Stdout, "No shared files available from %s.\n", peerArg)
		return
	}

	fmt.Printf("Shared files from %s:\n", peerArg)
	fmt.Print(text)
}

func runShare(args []string) {
	if len(args) == 0 {
		fmt.Println("Usage: shurli share <add|remove|list|deny> ...")
		osExit(1)
	}

	switch args[0] {
	case "add":
		runShareAdd(args[1:])
	case "remove", "rm":
		runShareRemove(args[1:])
	case "list", "ls":
		runShareList(args[1:])
	case "deny":
		runShareDeny(args[1:])
	default:
		runShareAdd(args)
	}
}

func runShareDeny(args []string) {
	fs := flag.NewFlagSet("share deny", flag.ExitOnError)
	jsonFlag := fs.Bool("json", false, "output as JSON")
	fs.Parse(reorderFlags(fs, args))

	remaining := fs.Args()
	if len(remaining) < 2 {
		fmt.Println("Usage: shurli share deny <path> <peer>")
		osExit(1)
	}

	absPath, err := filepath.Abs(remaining[0])
	if err != nil {
		fatal("Invalid path: %v", err)
	}

	peerName := remaining[1]

	client := requireClient()
	if err := client.ShareDeny(absPath, peerName); err != nil {
		fatal("Deny failed: %v", err)
	}

	if *jsonFlag {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(map[string]string{"status": "denied", "path": absPath, "peer": peerName})
		return
	}
	tc.Wgreen(os.Stdout, "Denied: %s removed from %s\n", peerName, absPath)
}

func runShareAdd(args []string) {
	fs := flag.NewFlagSet("share add", flag.ExitOnError)
	toFlag := fs.String("to", "", "peer name or ID")
	peersFlag := fs.String("peers", "", "comma-separated peer names or IDs")
	persistFlag := fs.Bool("persist", false, "persist share across restarts (default: true, configurable)")
	noPersistFlag := fs.Bool("no-persist", false, "do not persist share across restarts")
	jsonFlag := fs.Bool("json", false, "output as JSON")
	fs.Parse(reorderFlags(fs, args))

	if *toFlag != "" && *peersFlag != "" {
		fmt.Fprintln(os.Stderr, "Error: use --to or --peers, not both")
		osExit(1)
	}
	if *persistFlag && *noPersistFlag {
		fmt.Fprintln(os.Stderr, "Error: use --persist or --no-persist, not both")
		osExit(1)
	}

	remaining := fs.Args()
	if len(remaining) < 1 {
		fmt.Println("Usage: shurli share add <path> [--to <peer>] [--json]")
		osExit(1)
	}

	absPath, err := filepath.Abs(remaining[0])
	if err != nil {
		fatal("Invalid path: %v", err)
	}
	if _, err := os.Stat(absPath); err != nil {
		fatal("Cannot access path: %v", err)
	}

	var peers []string
	if *toFlag != "" {
		peers = []string{*toFlag}
	} else if *peersFlag != "" {
		peers = strings.Split(*peersFlag, ",")
	}

	// nil = let daemon use config default. Explicit flag overrides.
	var persistent *bool
	if *persistFlag {
		v := true
		persistent = &v
	} else if *noPersistFlag {
		v := false
		persistent = &v
	}

	client := requireClient()
	warnings, err := client.ShareAdd(absPath, peers, persistent)
	if err != nil {
		fatal("Share failed: %v", err)
	}

	if *jsonFlag {
		resp := map[string]any{"status": "shared", "path": absPath}
		if len(warnings) > 0 {
			resp["warnings"] = warnings
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(resp)
		return
	}

	tc.Wgreen(os.Stdout, "Shared: %s\n", absPath)
	if len(peers) > 0 {
		tc.Wfaint(os.Stdout, "  Added peer(s): %s\n", strings.Join(peers, ", "))
	} else {
		tc.Wfaint(os.Stdout, "  Visible to all authorized peers\n")
	}
	tc.Wfaint(os.Stdout, "  Use 'shurli share list' to see full peer list\n")
	for _, w := range warnings {
		tc.Wyellow(os.Stdout, "\n  Note: %s\n", w)
	}
}

func runShareRemove(args []string) {
	fs := flag.NewFlagSet("share remove", flag.ExitOnError)
	jsonFlag := fs.Bool("json", false, "output as JSON")
	fs.Parse(reorderFlags(fs, args))

	remaining := fs.Args()
	if len(remaining) < 1 {
		fmt.Println("Usage: shurli share remove <path> [--json]")
		osExit(1)
	}

	absPath, err := filepath.Abs(remaining[0])
	if err != nil {
		fatal("Invalid path: %v", err)
	}

	client := requireClient()
	if err := client.ShareRemove(absPath); err != nil {
		fatal("Unshare failed: %v", err)
	}

	if *jsonFlag {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(map[string]string{"status": "unshared", "path": absPath})
		return
	}
	tc.Wgreen(os.Stdout, "Unshared: %s\n", absPath)
}

func runShareList(args []string) {
	fs := flag.NewFlagSet("share list", flag.ExitOnError)
	jsonFlag := fs.Bool("json", false, "output as JSON")
	fs.Parse(reorderFlags(fs, args))

	client := requireClient()

	if *jsonFlag {
		shares, err := client.ShareList()
		if err != nil {
			fatal("List shares failed: %v", err)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(shares)
		return
	}

	text, err := client.ShareListText()
	if err != nil {
		fatal("List shares failed: %v", err)
	}

	if text == "" {
		tc.Wfaint(os.Stdout, "No paths currently shared.\n")
		tc.Wfaint(os.Stdout, "Share a path: shurli share add /path/to/file\n")
		return
	}
	fmt.Print(text)
}

func runTransfers(args []string) {
	fs := flag.NewFlagSet("transfers", flag.ExitOnError)
	jsonFlag := fs.Bool("json", false, "output as JSON")
	watchFlag := fs.Bool("watch", false, "live feed (refreshes every 2s)")
	historyFlag := fs.Bool("history", false, "show transfer event history")
	maxFlag := fs.Int("max", 50, "max events with --history")
	fs.Parse(reorderFlags(fs, args))

	client := requireClient()

	if *historyFlag {
		showTransferHistory(client, *maxFlag, *jsonFlag)
		return
	}

	if *watchFlag {
		watchTransfers(client, *jsonFlag)
		return
	}

	transfers, err := client.TransferList()
	if err != nil {
		fatal("Failed to list transfers: %v", err)
	}

	pending, pendingErr := client.TransferPending()
	if pendingErr != nil {
		if !strings.Contains(pendingErr.Error(), "404") && !strings.Contains(pendingErr.Error(), "not found") {
			fmt.Fprintf(os.Stderr, "Warning: could not fetch pending transfers: %v\n", pendingErr)
		}
	}
	for _, p := range pending {
		t, _ := time.Parse(time.RFC3339, p.Time)
		transfers = append(transfers, TransferSnapshot{
			ID:        p.ID,
			Filename:  p.Filename,
			Size:      p.Size,
			PeerID:    p.PeerID,
			Direction: "receive",
			Status:    "awaiting_approval",
			StartTime: t,
		})
	}

	if *jsonFlag {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(transfers)
		return
	}

	if len(transfers) == 0 {
		tc.Wfaint(os.Stdout, "No transfers.\n")
		return
	}

	printTransferTable(transfers)
}

func showTransferHistory(client *daemonClient, max int, jsonOutput bool) {
	events, err := client.TransferHistory(max)
	if err != nil {
		fatal("Failed to get transfer history: %v", err)
	}

	if jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(events)
		return
	}

	if len(events) == 0 {
		tc.Wfaint(os.Stdout, "No transfer history.\n")
		return
	}

	for _, ev := range events {
		ts := ev.Timestamp.Local().Format("2006-01-02 15:04:05")
		dir := "\u2191"
		if ev.Direction == "receive" {
			dir = "\u2193"
		}

		sizeStr := ""
		if ev.FileSize > 0 {
			sizeStr = humanSize(ev.FileSize)
		}

		fmt.Printf("  %s  %s %s  %-18s  %s", ts, dir, SanitizeDisplayName(ev.FileName), ev.EventType, sizeStr)
		if ev.Duration != "" {
			fmt.Printf("  %s", ev.Duration)
		}
		if ev.Error != "" {
			fmt.Printf("  ")
			tc.Wred(os.Stdout, "%s", sdk.HumanizeError(ev.Error))
		}
		peerShort := ev.PeerID
		if len(peerShort) > 16 {
			peerShort = peerShort[:16] + "..."
		}
		fmt.Printf("  %s", peerShort)
		fmt.Println()
	}
}

func printTransferTable(transfers []TransferSnapshot) {
	sort.Slice(transfers, func(i, j int) bool {
		if transfers[i].Done != transfers[j].Done {
			return !transfers[i].Done
		}
		return transfers[i].StartTime.After(transfers[j].StartTime)
	})

	for i := range transfers {
		t := &transfers[i]
		dir := "\u2191"
		if t.Direction == "receive" {
			dir = "\u2193"
		}

		peerShort := t.PeerID
		if len(peerShort) > 16 {
			peerShort = peerShort[:16] + "..."
		}

		pctStr := ""
		if t.Size > 0 && !t.Done {
			pct := float64(t.Transferred) / float64(t.Size) * 100
			pctStr = fmt.Sprintf(" %.0f%%", pct)
		}

		compressTag := ""
		if t.Compressed && t.CompressedSize > 0 && t.Size > 0 {
			ratio := float64(t.Size) / float64(t.CompressedSize)
			compressTag = fmt.Sprintf(" [zstd %.1f:1]", ratio)
		} else if t.Compressed {
			compressTag = " [zstd]"
		}

		erasureTag := ""
		if t.ErasureParity > 0 {
			erasureTag = fmt.Sprintf(" [RS %.0f%%, %d parity]",
				t.ErasureOverhead*100, t.ErasureParity)
		}

		// Use EndTime for completed/failed transfers so elapsed time freezes.
		var age time.Duration
		if !t.EndTime.IsZero() {
			age = t.EndTime.Sub(t.StartTime).Truncate(time.Second)
		} else {
			age = time.Since(t.StartTime).Truncate(time.Second)
		}
		if age < 0 {
			age = 0
		}

		name := truncateDisplay(SanitizeDisplayName(t.Filename), 20) // R2-F24

		fmt.Printf("  %s %s  %s  %s  %s/%s%s%s%s  ",
			dir, t.ID, name, peerShort,
			humanSize(t.Transferred), humanSize(t.Size),
			pctStr, compressTag, erasureTag,
		)

		switch t.Status {
		case "complete":
			tc.Wgreen(os.Stdout, "complete")
		case "failed":
			tc.Wred(os.Stdout, "failed")
		case "active":
			tc.Wyellow(os.Stdout, "active")
		case "pending":
			tc.Wfaint(os.Stdout, "pending")
		case "awaiting_approval":
			tc.Wcyan(os.Stdout, "awaiting approval")
		default:
			fmt.Print(t.Status)
		}

		// Show speed: average for completed, live for active.
		speedStr := ""
		if t.Transferred > 0 && age > 0 {
			bytesPerSec := float64(t.Transferred) / age.Seconds()
			speedStr = fmt.Sprintf("  %sps", humanSize(int64(bytesPerSec)))
		}

		fmt.Printf("  %s%s\n", age, speedStr)
		if t.Error != "" {
			tc.Wred(os.Stdout, "    error: %s\n", sdk.HumanizeError(t.Error))
		}
	}
}

// watchSnapshot tracks per-transfer state across --watch refresh cycles (R2-F21).
type watchSnapshot struct {
	transferred int64
	sampleCount int
}

func watchTransfers(client *daemonClient, jsonOutput bool) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	prevSnap := make(map[string]watchSnapshot)
	prevTime := time.Now()

	printWatchRound(client, jsonOutput, prevSnap, &prevTime)
	for range ticker.C {
		fmt.Print("\033[2J\033[H")
		printWatchRound(client, jsonOutput, prevSnap, &prevTime)
	}
}

func printWatchRound(client *daemonClient, jsonOutput bool, prevSnap map[string]watchSnapshot, prevTime *time.Time) {
	transfers, err := client.TransferList()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
		return
	}

	pending, _ := client.TransferPending()
	for _, p := range pending {
		t, _ := time.Parse(time.RFC3339, p.Time)
		transfers = append(transfers, TransferSnapshot{
			ID: p.ID, Filename: p.Filename, Size: p.Size, PeerID: p.PeerID,
			Direction: "receive", Status: "awaiting_approval", StartTime: t,
		})
	}

	if jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(transfers)
		return
	}

	tc.Wfaint(os.Stdout, "Transfers (live, Ctrl+C to exit)  %s\n\n", time.Now().Format("15:04:05"))
	if len(transfers) == 0 {
		tc.Wfaint(os.Stdout, "No transfers.\n")
		for k := range prevSnap {
			delete(prevSnap, k)
		}
		return
	}

	now := time.Now()
	dt := now.Sub(*prevTime).Seconds()
	printWatchTable(transfers, prevSnap, dt)

	// Update state for next round.
	seen := make(map[string]bool)
	for _, t := range transfers {
		seen[t.ID] = true
		prev, exists := prevSnap[t.ID]
		if exists {
			prevSnap[t.ID] = watchSnapshot{transferred: t.Transferred, sampleCount: prev.sampleCount + 1}
		} else {
			prevSnap[t.ID] = watchSnapshot{transferred: t.Transferred, sampleCount: 0}
		}
	}
	// R3-F9: cleanup stale entries to prevent unbounded map growth.
	for id := range prevSnap {
		if !seen[id] {
			delete(prevSnap, id)
		}
	}
	*prevTime = now
}

// printWatchTable renders the transfer table with interval speed and ETA.
// Separate from printTransferTable (R3-F8) because it needs cross-cycle state.
func printWatchTable(transfers []TransferSnapshot, prevSnap map[string]watchSnapshot, dtSec float64) {
	sort.Slice(transfers, func(i, j int) bool {
		if transfers[i].Done != transfers[j].Done {
			return !transfers[i].Done
		}
		return transfers[i].StartTime.After(transfers[j].StartTime)
	})

	tw := termWidth()

	for i := range transfers {
		t := &transfers[i]
		dir := "\u2191"
		if t.Direction == "receive" {
			dir = "\u2193"
		}

		peerShort := t.PeerID
		if len(peerShort) > 16 {
			peerShort = peerShort[:16] + "..."
		}

		name := truncateDisplay(SanitizeDisplayName(t.Filename), 20) // R2-F24

		pctStr := ""
		if t.Size > 0 && !t.Done {
			pct := float64(t.Transferred) / float64(t.Size) * 100
			pctStr = fmt.Sprintf(" %.0f%%", pct)
		}

		// R3-F10: adaptive field dropping based on terminal width.
		compressTag := ""
		if tw >= 100 {
			if t.Compressed && t.CompressedSize > 0 && t.Size > 0 {
				ratio := float64(t.Size) / float64(t.CompressedSize)
				compressTag = fmt.Sprintf(" [zstd %.1f:1]", ratio)
			} else if t.Compressed {
				compressTag = " [zstd]"
			}
		}

		erasureTag := ""
		if tw >= 120 && t.ErasureParity > 0 {
			erasureTag = fmt.Sprintf(" [RS %.0f%%, %d parity]",
				t.ErasureOverhead*100, t.ErasureParity)
		}

		var age time.Duration
		if !t.EndTime.IsZero() {
			age = t.EndTime.Sub(t.StartTime).Truncate(time.Second)
		} else {
			age = time.Since(t.StartTime).Truncate(time.Second)
		}
		if age < 0 {
			age = 0
		}

		// Interval speed for active transfers, lifetime avg for completed/first refresh.
		speedStr := ""
		etaStr := ""
		prev, hasPrev := prevSnap[t.ID]
		if t.Status == "active" && hasPrev && dtSec > 0.5 {
			bytesDelta := t.Transferred - prev.transferred
			if bytesDelta > 0 {
				intervalSpeed := float64(bytesDelta) / dtSec
				speedStr = fmt.Sprintf("  %s/s", humanSize(int64(intervalSpeed)))
				// R2-F22: show ETA only after 3+ refresh cycles (6s of data).
				if tw >= 140 && prev.sampleCount >= 2 && t.Size > 0 {
					remaining := t.Size - t.Transferred
					if remaining > 0 {
						etaSec := float64(remaining) / intervalSpeed
						if etaFmt := formatETA(etaSec); etaFmt != "" {
							etaStr = fmt.Sprintf("  ETA %s", etaFmt)
						}
					}
				}
			}
		} else if t.Transferred > 0 && age > 0 {
			bytesPerSec := float64(t.Transferred) / age.Seconds()
			speedStr = fmt.Sprintf("  %s/s", humanSize(int64(bytesPerSec)))
		}

		fmt.Printf("  %s %s  %s  %s  %s/%s%s%s%s  ",
			dir, t.ID, name, peerShort,
			humanSize(t.Transferred), humanSize(t.Size),
			pctStr, compressTag, erasureTag,
		)

		switch t.Status {
		case "complete":
			tc.Wgreen(os.Stdout, "complete")
		case "failed":
			tc.Wred(os.Stdout, "failed")
		case "active":
			tc.Wyellow(os.Stdout, "active")
		case "pending":
			tc.Wfaint(os.Stdout, "pending")
		case "awaiting_approval":
			tc.Wcyan(os.Stdout, "awaiting approval")
		default:
			fmt.Print(t.Status)
		}

		fmt.Printf("  %s%s%s\n", age, speedStr, etaStr)
		if t.Error != "" {
			tc.Wred(os.Stdout, "    error: %s\n", sdk.HumanizeError(t.Error))
		}
	}
}

func runAccept(args []string) {
	fs := flag.NewFlagSet("accept", flag.ExitOnError)
	destFlag := fs.String("dest", "", "save to a specific directory")
	jsonFlag := fs.Bool("json", false, "output as JSON")
	allFlag := fs.Bool("all", false, "accept all pending transfers")
	fs.Parse(reorderFlags(fs, args))

	remaining := fs.Args()
	if !*allFlag && len(remaining) < 1 {
		fmt.Println("Usage: shurli accept <id> [--dest /path/] [--json]")
		fmt.Println("       shurli accept --all [--dest /path/] [--json]")
		osExit(1)
	}

	client := requireClient()

	if *allFlag {
		pending, err := client.TransferPending()
		if err != nil {
			fatal("Failed to list pending transfers: %v", err)
		}
		if len(pending) == 0 {
			tc.Wfaint(os.Stdout, "No pending transfers.\n")
			return
		}
		for _, p := range pending {
			if err := client.TransferAccept(p.ID, *destFlag); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: accept %s failed: %v\n", p.ID, err)
				continue
			}
			if *jsonFlag {
				json.NewEncoder(os.Stdout).Encode(map[string]string{"status": "accepted", "id": p.ID})
			} else {
				tc.Wgreen(os.Stdout, "Accepted")
				fmt.Printf(" %s (%s from %s)\n", p.ID, SanitizeDisplayName(p.Filename), p.PeerID)
			}
		}
		return
	}

	id := remaining[0]
	if err := client.TransferAccept(id, *destFlag); err != nil {
		if *jsonFlag {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			enc.Encode(map[string]string{"error": err.Error()})
		} else {
			fatal("Accept failed: %v", err)
		}
		return
	}

	if *jsonFlag {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(map[string]string{"status": "accepted", "id": id})
	} else {
		tc.Wgreen(os.Stdout, "Accepted")
		fmt.Printf(" transfer %s\n", id)
	}
}

func runReject(args []string) {
	fs := flag.NewFlagSet("reject", flag.ExitOnError)
	reasonFlag := fs.String("reason", "", "reject reason: space, busy, or size")
	jsonFlag := fs.Bool("json", false, "output as JSON")
	allFlag := fs.Bool("all", false, "reject all pending transfers")
	fs.Parse(reorderFlags(fs, args))

	remaining := fs.Args()
	if !*allFlag && len(remaining) < 1 {
		fmt.Println("Usage: shurli reject <id> [--reason space|busy|size] [--json]")
		fmt.Println("       shurli reject --all [--json]")
		osExit(1)
	}

	reason := *reasonFlag
	if reason != "" && reason != "space" && reason != "busy" && reason != "size" {
		fatal("Invalid reason %q. Must be: space, busy, or size", reason)
	}

	client := requireClient()

	if *allFlag {
		pending, err := client.TransferPending()
		if err != nil {
			fatal("Failed to list pending transfers: %v", err)
		}
		if len(pending) == 0 {
			tc.Wfaint(os.Stdout, "No pending transfers.\n")
			return
		}
		for _, p := range pending {
			if err := client.TransferReject(p.ID, reason); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: reject %s failed: %v\n", p.ID, err)
				continue
			}
			if *jsonFlag {
				json.NewEncoder(os.Stdout).Encode(map[string]string{"status": "rejected", "id": p.ID, "reason": reason})
			} else {
				tc.Wfaint(os.Stdout, "Rejected")
				fmt.Printf(" %s (%s from %s)\n", p.ID, SanitizeDisplayName(p.Filename), p.PeerID)
			}
		}
		return
	}

	id := remaining[0]
	if err := client.TransferReject(id, reason); err != nil {
		if *jsonFlag {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			enc.Encode(map[string]string{"error": err.Error()})
		} else {
			fatal("Reject failed: %v", err)
		}
		return
	}

	if *jsonFlag {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(map[string]string{"status": "rejected", "id": id, "reason": reason})
	} else {
		tc.Wfaint(os.Stdout, "Rejected")
		if reason != "" {
			fmt.Printf(" transfer %s (reason: %s)\n", id, reason)
		} else {
			fmt.Printf(" transfer %s\n", id)
		}
	}
}

func runCancel(args []string) {
	fs := flag.NewFlagSet("cancel", flag.ExitOnError)
	jsonFlag := fs.Bool("json", false, "output as JSON")
	fs.Parse(reorderFlags(fs, args))

	remaining := fs.Args()
	if len(remaining) < 1 {
		fmt.Println("Usage: shurli cancel <id> [--json]")
		osExit(1)
	}

	client := requireClient()
	id := remaining[0]

	if err := client.CancelTransfer(id); err != nil {
		if *jsonFlag {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			enc.Encode(map[string]string{"error": err.Error()})
		} else {
			fatal("Cancel failed: %v", err)
		}
		return
	}

	if *jsonFlag {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(map[string]string{"status": "cancelled", "id": id})
	} else {
		tc.Wfaint(os.Stdout, "Cancelled")
		fmt.Printf(" transfer %s\n", id)
	}
}

func runClean(args []string) {
	fs := flag.NewFlagSet("clean", flag.ExitOnError)
	jsonFlag := fs.Bool("json", false, "output as JSON")
	fs.Parse(reorderFlags(fs, args))

	client := requireClient()

	count, bytes, err := client.CleanTempFiles()
	if err != nil {
		if *jsonFlag {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			enc.Encode(map[string]string{"error": err.Error()})
		} else {
			fatal("Clean failed: %v", err)
		}
		return
	}

	if *jsonFlag {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(map[string]any{"files_removed": count, "bytes_freed": bytes})
	} else {
		if count == 0 {
			fmt.Println("No temporary files to clean.")
		} else {
			fmt.Printf("Cleaned %d temp file(s), freed %s\n", count, humanSize(bytes))
		}
	}
}
