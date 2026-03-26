package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shurlinet/shurli/internal/auth"
	"github.com/shurlinet/shurli/internal/config"
	"github.com/shurlinet/shurli/internal/daemon"
	tc "github.com/shurlinet/shurli/internal/termcolor"
	"github.com/shurlinet/shurli/internal/validate"
	"github.com/shurlinet/shurli/pkg/p2pnet"
)

func runStatus(args []string) {
	if err := doStatus(args, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doStatus(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configFlag := fs.String("config", "", "path to config file")
	if err := fs.Parse(reorderFlags(fs, args)); err != nil {
		return err
	}

	// Version
	tc.Wfaint(stdout, "shurli %s (%s) built %s\n", version, commit, buildDate)
	fmt.Fprintln(stdout)

	// Find and load config
	cfgFile, err := config.FindConfigFile(*configFlag)
	if err != nil {
		tc.Wblue(stdout, "Config:   ")
		fmt.Fprintf(stdout, "not found (%v)\n", err)
		fmt.Fprintln(stdout)
		fmt.Fprintln(stdout, "Run 'shurli init' to create a configuration.")
		return fmt.Errorf("config not found: %w", err)
	}
	cfg, err := config.LoadNodeConfig(cfgFile)
	if err != nil {
		return fmt.Errorf("config error: %w", err)
	}
	config.ResolveConfigPaths(cfg, filepath.Dir(cfgFile))

	// Peer ID
	pw, _ := resolvePassword(filepath.Dir(cfgFile))
	peerID, err := p2pnet.PeerIDFromKeyFile(cfg.Identity.KeyFile, pw)
	tc.Wblue(stdout, "Peer ID:  ")
	if err != nil {
		fmt.Fprintf(stdout, "error (%v)\n", err)
	} else {
		fmt.Fprintf(stdout, "%s\n", peerID)
	}
	tc.Wblue(stdout, "Config:   ")
	fmt.Fprintf(stdout, "%s\n", cfgFile)
	tc.Wblue(stdout, "Key file: ")
	fmt.Fprintf(stdout, "%s\n", cfg.Identity.KeyFile)
	tc.Wblue(stdout, "Network:  ")
	if cfg.Discovery.Network != "" {
		fmt.Fprintf(stdout, "%s\n", cfg.Discovery.Network)
	} else {
		fmt.Fprintf(stdout, "global (default)\n")
	}

	// Daemon status
	var daemonStatus *daemon.StatusResponse
	tc.Wblue(stdout, "Daemon:   ")
	if c := tryDaemonClient(); c != nil {
		resp, err := c.Status()
		if err == nil {
			daemonStatus = resp
			tc.Wgreen(stdout, "running")
			uptime := (time.Duration(resp.UptimeSeconds) * time.Second).Truncate(time.Second)
			tc.Wfaint(stdout, " (uptime: %s, peers: %d", uptime, resp.ConnectedPeers)
			if resp.Reachability != nil {
				tc.Wfaint(stdout, ", reachability: ")
				writeReachabilityGrade(stdout, resp.Reachability)
			}
			tc.Wfaint(stdout, ")")
			fmt.Fprintln(stdout)
		} else {
			tc.Wyellow(stdout, "not responding\n")
		}
	} else {
		tc.Wred(stdout, "not running\n")
	}
	fmt.Fprintln(stdout)

	// Reachability details (when daemon is running and grade available)
	if daemonStatus != nil && daemonStatus.Reachability != nil {
		r := daemonStatus.Reachability
		tc.Wblue(stdout, "Reachability: ")
		writeReachabilityGrade(stdout, r)
		tc.Wfaint(stdout, " - %s", r.Description)
		fmt.Fprintln(stdout)
		fmt.Fprintln(stdout)
	}

	// Relays (with connectivity when daemon is running)
	if daemonStatus != nil && len(daemonStatus.Relays) > 0 {
		fmt.Fprintln(stdout, "Relays:")
		for _, r := range daemonStatus.Relays {
			fmt.Fprint(stdout, "  ")
			if r.Connected {
				tc.Wgreen(stdout, "[connected]   ")
			} else {
				tc.Wred(stdout, "[disconnected]")
			}
			fmt.Fprint(stdout, "  ")
			fmt.Fprint(stdout, r.Address)
			if r.AgentVersion != "" {
				tc.Wfaint(stdout, "  %s", validate.SanitizeForDisplay(r.AgentVersion))
			}
			fmt.Fprintln(stdout)
		}

		// MOTD/goodbye messages
		if len(daemonStatus.MOTDs) > 0 {
			fmt.Fprintln(stdout)
			fmt.Fprintln(stdout, "Messages:")
			for _, m := range daemonStatus.MOTDs {
				name := validate.SanitizeForDisplay(m.RelayName)
				if name == "" {
					pid := m.RelayPeerID
					if len(pid) > 16 {
						pid = pid[:16] + "..."
					}
					name = pid
				}
				ts, _ := time.Parse(time.RFC3339, m.Timestamp)
				ago := formatTimeAgo(ts)
				fmt.Fprint(stdout, "  ")
				tc.Wyellow(stdout, "[%s]", m.Type)
				fmt.Fprintf(stdout, " %s: ", name)
				tc.Wfaint(stdout, "%q  (%s)", m.Message, ago)
				fmt.Fprintln(stdout)
			}
		} else {
			fmt.Fprintln(stdout)
			tc.Wfaint(stdout, "Messages: (none)\n")
		}
	} else if len(cfg.Relay.Addresses) > 0 {
		fmt.Fprintln(stdout, "Relay addresses:")
		for _, addr := range cfg.Relay.Addresses {
			fmt.Fprintf(stdout, "  %s\n", addr)
		}
	} else {
		tc.Wfaint(stdout, "Relay addresses: (none configured)\n")
	}
	// Relay grants (client-side cached receipts from relays).
	if daemonStatus != nil && len(daemonStatus.RelayGrants) > 0 {
		fmt.Fprintln(stdout)
		fmt.Fprintln(stdout, "Relay Grants:")
		for _, rg := range daemonStatus.RelayGrants {
			name := validate.SanitizeForDisplay(rg.RelayName)
			if name == "" {
				pid := rg.RelayPeerID
				if len(pid) > 16 {
					pid = pid[:16] + "..."
				}
				name = pid
			}
			// No cached grant for this relay - signaling only.
			if rg.Remaining == "" && !rg.Permanent {
				fmt.Fprint(stdout, "  ")
				tc.Wfaint(stdout, "%s: no grant (signaling only)\n", name)
				continue
			}
			fmt.Fprintf(stdout, "  %s: ", name)
			if rg.Permanent {
				tc.Wgreen(stdout, "permanent")
			} else {
				tc.Wgreen(stdout, "active")
				fmt.Fprintf(stdout, ", expires in %s", rg.Remaining)
			}
			fmt.Fprintf(stdout, ", %s/session", rg.SessionBudget)
			if rg.SessionUsed != "" {
				tc.Wfaint(stdout, " (%s used)", rg.SessionUsed)
			}
			if rg.SessionDuration != "" {
				fmt.Fprintf(stdout, ", %s/circuit", rg.SessionDuration)
			}
			fmt.Fprintln(stdout)
		}
	}

	// Notifications section: configured sinks + expiring grants.
	if daemonStatus != nil && (daemonStatus.Notifications != nil || len(daemonStatus.ExpiringGrants) > 0) {
		fmt.Fprintln(stdout)
		fmt.Fprintln(stdout, "Notifications:")
		if daemonStatus.Notifications != nil {
			fmt.Fprintf(stdout, "  Sinks: %s\n", strings.Join(daemonStatus.Notifications.Sinks, ", "))
		}
		if len(daemonStatus.ExpiringGrants) > 0 {
			tc.Wyellow(stdout, "  Expiring grants:\n")
			for _, g := range daemonStatus.ExpiringGrants {
				fmt.Fprintf(stdout, "    %s expires in %s. Extend: shurli auth extend %s --duration 1h\n",
					g.Peer, g.Remaining, g.Peer)
			}
		}
	}

	fmt.Fprintln(stdout)

	// Authorized peers
	if cfg.Security.AuthorizedKeysFile != "" {
		peers, err := auth.ListPeers(cfg.Security.AuthorizedKeysFile)
		if err != nil {
			fmt.Fprintf(stdout, "Authorized peers: error (%v)\n", err)
		} else if len(peers) == 0 {
			tc.Wfaint(stdout, "Authorized peers: (none)\n")
		} else {
			fmt.Fprintf(stdout, "Authorized peers (%d):\n", len(peers))
			for _, p := range peers {
				short := p.PeerID
				if len(short) > 16 {
					short = short[:16] + "..."
				}
				fmt.Fprint(stdout, "  ")
				if p.Verified != "" {
					tc.Wgreen(stdout, "[VERIFIED]  ")
				} else {
					tc.Wyellow(stdout, "[UNVERIFIED]")
					fmt.Fprint(stdout, " ")
				}
				fmt.Fprint(stdout, short)
				if p.Comment != "" {
					tc.Wfaint(stdout, "  # %s", validate.SanitizeForDisplay(p.Comment))
				}
				fmt.Fprintln(stdout)
			}
		}
	} else {
		tc.Wfaint(stdout, "Authorized peers: connection gating disabled\n")
	}
	fmt.Fprintln(stdout)

	// Services
	if cfg.Services != nil && len(cfg.Services) > 0 {
		fmt.Fprintln(stdout, "Services:")
		for name, svc := range cfg.Services {
			fmt.Fprint(stdout, "  ")
			fmt.Fprintf(stdout, "%-12s -> %-20s ", name, svc.LocalAddress)
			if svc.Enabled {
				tc.Wgreen(stdout, "(enabled)")
			} else {
				tc.Wfaint(stdout, "(disabled)")
			}
			fmt.Fprintln(stdout)
		}
	} else {
		tc.Wfaint(stdout, "Services: (none configured)\n")
	}
	fmt.Fprintln(stdout)

	// Names
	if cfg.Names != nil && len(cfg.Names) > 0 {
		fmt.Fprintln(stdout, "Names:")
		for name, peerIDStr := range cfg.Names {
			short := peerIDStr
			if len(short) > 16 {
				short = short[:16] + "..."
			}
			fmt.Fprintf(stdout, "  %-12s -> %s\n", name, short)
		}
	} else {
		tc.Wfaint(stdout, "Names: (none configured)\n")
	}
	return nil
}

// writeReachabilityGrade writes a colorized reachability grade (e.g., "A Excellent").
func writeReachabilityGrade(w io.Writer, r *p2pnet.ReachabilityGrade) {
	grade := r.Grade + " " + r.Label
	switch r.Grade {
	case "A":
		tc.Wgreen(w, "%s", grade)
	case "B":
		tc.Wgreen(w, "%s", grade)
	case "C":
		tc.Wyellow(w, "%s", grade)
	case "D":
		tc.Wyellow(w, "%s", grade)
	case "F":
		tc.Wred(w, "%s", grade)
	default:
		fmt.Fprint(w, grade)
	}
}

func formatTimeAgo(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
