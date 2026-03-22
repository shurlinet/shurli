package main

import (
	"crypto/sha256"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/crypto/hkdf"

	"github.com/shurlinet/shurli/internal/config"
	"github.com/shurlinet/shurli/internal/grants"
	"github.com/shurlinet/shurli/internal/termcolor"
	"github.com/shurlinet/shurli/pkg/p2pnet"
)

func runAuthAudit(args []string) {
	if err := doAuthAudit(args, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doAuthAudit(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("auth audit", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	verify := fs.Bool("verify", false, "verify chain integrity (no output on success)")
	tail := fs.Int("tail", 20, "number of recent entries to show")
	configFlag := fs.String("config", "", "path to config file")
	if err := fs.Parse(reorderArgs(args, nil)); err != nil {
		return err
	}

	// Resolve config to find the audit log path and derive the HMAC key.
	cfgFile, err := config.FindConfigFile(*configFlag)
	if err != nil {
		return fmt.Errorf("config error: %w", err)
	}
	cfg, err := config.LoadNodeConfig(cfgFile)
	if err != nil {
		return fmt.Errorf("config error: %w", err)
	}
	config.ResolveConfigPaths(cfg, filepath.Dir(cfgFile))

	// Load identity key (needed for HKDF derivation of audit HMAC key).
	// Guard: verify key file exists before loading. LoadOrCreateIdentity
	// would create a new identity as a side effect, which is wrong for
	// a read-only audit command.
	configDir := filepath.Dir(cfgFile)
	if _, err := os.Stat(cfg.Identity.KeyFile); os.IsNotExist(err) {
		return fmt.Errorf("identity key file does not exist: %s\n  Run 'shurli init' first", cfg.Identity.KeyFile)
	}

	pw, err := resolvePasswordInteractive(configDir, stdout)
	if err != nil {
		return fmt.Errorf("cannot resolve identity password: %w", err)
	}

	privKey, err := p2pnet.LoadOrCreateIdentity(cfg.Identity.KeyFile, pw)
	if err != nil {
		return fmt.Errorf("cannot load identity key: %w", err)
	}

	raw, err := privKey.Raw()
	if err != nil || len(raw) < 32 {
		return fmt.Errorf("cannot extract identity seed")
	}
	auditKey := hkdfDerive(raw[:32], "shurli/grants/audit/v1")
	if auditKey == nil {
		return fmt.Errorf("failed to derive audit key")
	}

	auditPath := filepath.Join(configDir, "grant_audit.log")
	al, err := grants.NewAuditLog(auditPath, auditKey)
	if err != nil {
		return fmt.Errorf("cannot open audit log: %w", err)
	}

	if *verify {
		count, err := al.Verify()
		if err != nil {
			termcolor.Red("Audit log INTEGRITY FAILURE")
			fmt.Fprintf(stdout, "  Verified: %d entries before failure\n", count)
			return err
		}
		termcolor.Green("Audit log integrity verified")
		fmt.Fprintf(stdout, "  Entries: %d\n", count)
		fmt.Fprintf(stdout, "  File:    %s\n", auditPath)
		return nil
	}

	// Show recent entries.
	entries, err := al.Entries()
	if err != nil {
		return fmt.Errorf("read audit log: %w", err)
	}

	if len(entries) == 0 {
		fmt.Fprintln(stdout, "No audit log entries.")
		fmt.Fprintf(stdout, "\nFile: %s\n", auditPath)
		return nil
	}

	// Tail the entries.
	start := 0
	if *tail > 0 && *tail < len(entries) {
		start = len(entries) - *tail
	}

	fmt.Fprintf(stdout, "Grant audit log (%d entries, showing last %d):\n\n", len(entries), len(entries)-start)
	for _, e := range entries[start:] {
		ts := e.Timestamp.Format("2006-01-02 15:04:05")
		fmt.Fprintf(stdout, "  %s  %-18s  peer:%s", ts, e.Event, e.PeerID)
		if exp, ok := e.Metadata["expires_at"]; ok {
			fmt.Fprintf(stdout, "  expires:%s", exp)
		}
		if used, ok := e.Metadata["refreshes_used"]; ok {
			if max, ok2 := e.Metadata["max_refreshes"]; ok2 {
				fmt.Fprintf(stdout, "  refresh:%s/%s", used, max)
			}
		}
		fmt.Fprintln(stdout)
	}
	fmt.Fprintf(stdout, "\nFile: %s\n", auditPath)
	return nil
}

// hkdfDerive derives a 32-byte key from seed using HKDF-SHA256.
func hkdfDerive(seed []byte, domain string) []byte {
	r := hkdf.New(sha256.New, seed, nil, []byte(domain))
	key := make([]byte, 32)
	if _, err := io.ReadFull(r, key); err != nil {
		return nil
	}
	return key
}
