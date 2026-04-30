package filetransfer

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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
			Usage:       "shurli download <peer>:<path> [--dest dir] [--files 1,3] [--exclude 2] [--list] [--json]",
			Run:         runDownload,
			Flags: []plugin.CLIFlagEntry{
				{Long: "json", Type: "bool", Description: "Output as JSON"},
				{Long: "dest", Type: "directory", Description: "Local directory to save into", RequiresArg: true},
				{Long: "follow", Type: "bool", Description: "Follow transfer progress"},
				{Long: "quiet", Type: "bool", Description: "Show only a single progress bar"},
				{Long: "silent", Type: "bool", Description: "No progress output"},
				{Long: "multi-peer", Type: "bool", Description: "Enable multi-peer download (requires --peers)"},
				{Long: "peers", Type: "string", Description: "Extra peer names/IDs for multi-peer download (comma-separated, implies --multi-peer)", RequiresArg: true},
				{Long: "files", Type: "string", Description: "Download only these files (1-indexed, comma-separated, ranges: 1-5,10)", RequiresArg: true},
				{Long: "exclude", Type: "string", Description: "Download all except these files (1-indexed, ranges: 1-5,10)", RequiresArg: true},
				{Long: "list", Type: "bool", Description: "List downloadable files with indices (for use with --files/--exclude)"},
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
			Usage:       "shurli accept <id|--all> [--dest dir] [--files 1,3] [--exclude 2] [--json]",
			Run:         runAccept,
			Flags: []plugin.CLIFlagEntry{
				{Long: "all", Type: "bool", Description: "Accept all pending transfers"},
				{Long: "dest", Type: "directory", Description: "Save to a specific directory", RequiresArg: true},
				{Long: "files", Type: "string", Description: "Accept only these files (indices: 1-5,10 or patterns: '*.jpg')", RequiresArg: true},
				{Long: "exclude", Type: "string", Description: "Accept all except these files (indices: 1-5,10 or patterns: '*.raw')", RequiresArg: true},
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
	filesFlag := fs.String("files", "", "download only these files (1-indexed, comma-separated, ranges: 1-5,10)")
	excludeFlag := fs.String("exclude", "", "download all except these files (1-indexed, ranges: 1-5,10)")
	listFlag := fs.Bool("list", false, "list downloadable files with indices (for use with --files/--exclude)")
	fs.Parse(reorderFlags(fs, args))

	remaining := fs.Args()
	if len(remaining) < 1 {
		fmt.Println("Usage: shurli download <peer>:<shareID/filename> [--dest /local/dir] [--follow] [--list] [--json]")
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

	// #41: --list mode — list files without downloading.
	if *listFlag && (*filesFlag != "" || *excludeFlag != "" || *multiPeerFlag || *extraPeersFlag != "") {
		fatal("--list cannot be combined with --files, --exclude, --multi-peer, or --peers")
	}
	if *listFlag {
		resp, err := client.DownloadList(peerArg, remotePath)
		if err != nil {
			fatal("List failed: %s", sdk.HumanizeError(err.Error()))
		}
		if *jsonFlag {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			enc.Encode(resp)
			return
		}
		fmt.Printf("Files available for download from %s:\n", peerArg)
		for _, f := range resp.Files {
			fmt.Printf("  %3d. %-60s %s\n", f.Index, SanitizeDisplayName(f.Path), humanSize(f.Size))
		}
		fmt.Printf("\n%d files, %s total\n", len(resp.Files), humanSize(resp.TotalSize))
		// R10-F6: note about mutable directories.
		fmt.Println()
		tc.Wfaint(os.Stdout, "Download all:     shurli download %s:%s\n", peerArg, remotePath)
		tc.Wfaint(os.Stdout, "Select files:     shurli download %s:%s --files 1,3,10-20\n", peerArg, remotePath)
		tc.Wfaint(os.Stdout, "Exclude files:    shurli download %s:%s --exclude 5-8\n", peerArg, remotePath)
		tc.Wfaint(os.Stdout, "Note: file indices may change if the shared directory is modified.\n")
		return
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

	// #18: validate and parse --files / --exclude for selective download.
	if *filesFlag != "" && *excludeFlag != "" {
		fatal("Cannot use both --files and --exclude (mutually exclusive)")
	}
	var dlFilesAPI, dlExcludeAPI []int
	if *filesFlag != "" {
		parsed, parseErr := parseIndexList(*filesFlag)
		if parseErr != nil {
			fatal("Invalid --files: %v", parseErr)
		}
		dlFilesAPI = cliToAPIIndices(parsed)
	}
	if *excludeFlag != "" {
		parsed, parseErr := parseIndexList(*excludeFlag)
		if parseErr != nil {
			fatal("Invalid --exclude: %v", parseErr)
		}
		dlExcludeAPI = cliToAPIIndices(parsed)
	}

	// F10: multi-peer + selective rejection = incompatible.
	if *multiPeerFlag && (dlFilesAPI != nil || dlExcludeAPI != nil) {
		fatal("Selective file rejection is not supported with multi-peer downloads")
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

	resp, err := client.Download(peerArg, remotePath, *destFlag, *multiPeerFlag, extraPeers, dlFilesAPI, dlExcludeAPI)
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

	// R8-F7: if positional arg is given, show single transfer details.
	if remaining := fs.Args(); len(remaining) > 0 {
		showSingleTransfer(client, remaining[0], *jsonFlag)
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
			ID:           p.ID,
			Filename:     p.Filename,
			Size:         p.Size,
			PeerID:       p.PeerID,
			Direction:    "receive",
			Status:       "awaiting_approval",
			StartTime:    t,
			PendingFiles: p.Files, // R7-F2: include file list in JSON output
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

	// R6-F20: show file list for pending multi-file transfers.
	// Critical for selective rejection UX - users need indices.
	printPendingFileLists(pending)
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
		// R8-F3: show selective rejection info in transfer history.
		if ev.AcceptedFiles > 0 && ev.TotalFiles > 0 && ev.AcceptedFiles < ev.TotalFiles {
			fmt.Printf("  (%d/%d files)", ev.AcceptedFiles, ev.TotalFiles)
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

// showSingleTransfer fetches and displays details for a single transfer by ID.
// R8-F7: supports both active and pending transfers (R8-F6 API fallthrough).
// For pending multi-file transfers, shows the full file list (no truncation).
func showSingleTransfer(client *daemonClient, id string, jsonOutput bool) {
	snap, err := client.TransferSnapshot(id)
	if err != nil {
		fatal("Transfer %q not found: %v", id, err)
	}

	if jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(snap)
		return
	}

	peerShort := snap.PeerID
	if len(peerShort) > 16 {
		peerShort = peerShort[:16] + "..."
	}

	fmt.Printf("Transfer: %s\n", snap.ID)
	fmt.Printf("  File:      %s\n", SanitizeDisplayName(snap.Filename))
	fmt.Printf("  Size:      %s\n", humanSize(snap.Size))
	fmt.Printf("  Peer:      %s\n", peerShort)
	fmt.Printf("  Direction: %s\n", snap.Direction)
	if snap.Transport != "" {
		fmt.Printf("  Transport: %s\n", snap.Transport)
	}
	fmt.Printf("  Status:    %s\n", snap.Status)

	if snap.Transferred > 0 {
		pct := float64(snap.Transferred) / float64(snap.Size) * 100
		fmt.Printf("  Progress:  %s / %s (%.0f%%)\n",
			humanSize(snap.Transferred), humanSize(snap.Size), pct)
	}

	// Show file list for pending multi-file transfers (full, no truncation).
	if len(snap.PendingFiles) > 1 {
		fmt.Printf("\n  Files (%d total):\n", len(snap.PendingFiles))
		for _, f := range snap.PendingFiles {
			fmt.Printf("    %d. %s  (%s)\n", f.Index+1, SanitizeDisplayName(f.Path), humanSize(f.Size))
		}
		fmt.Println()
		filesHint, excludeHint := selectiveHintIndices(len(snap.PendingFiles))
		tc.Wfaint(os.Stdout, "  Accept specific files: shurli accept %s --files %s\n", snap.ID, filesHint)
		tc.Wfaint(os.Stdout, "  Exclude files:         shurli accept %s --exclude %s\n", snap.ID, excludeHint)
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

		transportTag := ""
		if t.Transport == "tcp" {
			transportTag = " [tcp]"
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

		fmt.Printf("  %s %s  %s  %s  %s/%s%s%s%s%s  ",
			dir, t.ID, name, peerShort,
			humanSize(t.Transferred), humanSize(t.Size),
			pctStr, compressTag, erasureTag, transportTag,
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

// printPendingFileLists shows file lists for pending multi-file transfers (#18, R6-F20).
// Truncates to maxPendingFileDisplay files with "and N more" for large transfers.
// SEC-F5: all paths sanitized. R7-F1: not called from watch mode.
func printPendingFileLists(pending []PendingTransferInfo) {
	const maxPendingFileDisplay = 20
	for _, p := range pending {
		if len(p.Files) <= 1 {
			continue
		}
		fmt.Printf("\n  Files in %s (%d total):\n", SanitizeDisplayName(p.Filename), len(p.Files))
		limit := len(p.Files)
		if limit > maxPendingFileDisplay {
			limit = maxPendingFileDisplay
		}
		for i := 0; i < limit; i++ {
			f := p.Files[i]
			// R8-F5: display format with 1-indexed, sanitized path, human size.
			fmt.Printf("    %d. %s  (%s)\n", f.Index+1, SanitizeDisplayName(f.Path), humanSize(f.Size))
		}
		if len(p.Files) > maxPendingFileDisplay {
			tc.Wfaint(os.Stdout, "    ... and %d more\n", len(p.Files)-maxPendingFileDisplay)
		}
		if p.HasErasure {
			tc.Wfaint(os.Stdout, "    Note: selective rejection unavailable (sender uses erasure coding)\n")
		} else {
			filesHint, excludeHint := selectiveHintIndices(len(p.Files))
			tc.Wfaint(os.Stdout, "    Accept specific files: shurli accept %s --files %s\n", p.ID, filesHint)
			tc.Wfaint(os.Stdout, "    Exclude files:         shurli accept %s --exclude %s\n", p.ID, excludeHint)
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
	consecErrors := 0

	printWatchRound(client, jsonOutput, prevSnap, &prevTime, &consecErrors)
	for range ticker.C {
		if consecErrors >= 3 {
			fmt.Fprintf(os.Stderr, "\nDaemon unreachable after %d attempts. Exiting.\n", consecErrors)
			fmt.Fprintf(os.Stderr, "Run 'shurli transfers --watch' again after daemon restarts.\n")
			os.Exit(1)
		}
		fmt.Print("\033[2J\033[H")
		printWatchRound(client, jsonOutput, prevSnap, &prevTime, &consecErrors)
	}
}

func printWatchRound(client *daemonClient, jsonOutput bool, prevSnap map[string]watchSnapshot, prevTime *time.Time, consecErrors *int) {
	transfers, err := client.TransferList()
	if err != nil {
		// Unauthorized means daemon restarted with a new cookie — this
		// watch process is permanently stale. Exit immediately.
		if strings.Contains(err.Error(), "unauthorized") {
			fmt.Fprintf(os.Stderr, "\nDaemon restarted (auth token changed). Exiting.\n")
			fmt.Fprintf(os.Stderr, "Run 'shurli transfers --watch' again.\n")
			os.Exit(1)
		}
		*consecErrors++
		fmt.Fprintf(os.Stderr, "Warning: %v (attempt %d/3)\n", err, *consecErrors)
		return
	}
	*consecErrors = 0

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

		transportTag := ""
		if t.Transport == "tcp" {
			transportTag = " [tcp]"
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

		fmt.Printf("  %s %s  %s  %s  %s/%s%s%s%s%s  ",
			dir, t.ID, name, peerShort,
			humanSize(t.Transferred), humanSize(t.Size),
			pctStr, compressTag, erasureTag, transportTag,
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
	filesFlag := fs.String("files", "", "accept only these files (1-indexed, comma-separated, supports ranges: 1-5,10)")
	excludeFlag := fs.String("exclude", "", "accept all except these files (1-indexed, comma-separated, supports ranges)")
	fs.Parse(reorderFlags(fs, args))

	remaining := fs.Args()
	if !*allFlag && len(remaining) < 1 {
		fmt.Println("Usage: shurli accept <id> [--dest /path/] [--json]")
		fmt.Println("       shurli accept <id> --files 1,3,5 [--dest /path/]")
		fmt.Println("       shurli accept <id> --exclude 2,4 [--dest /path/]")
		fmt.Println("       shurli accept --all [--dest /path/] [--json]")
		fmt.Println()
		fmt.Println("File indices are 1-indexed. Use 'shurli transfers' to see file lists.")
		fmt.Println("Ranges supported: --files 1-5,10,15-20")
		fmt.Println("Glob patterns:   --exclude '*.raw' (quote to prevent shell expansion)")
		osExit(1)
	}

	// F3/R6-F4: validate flag combinations.
	if *filesFlag != "" && *excludeFlag != "" {
		fatal("Cannot use both --files and --exclude (mutually exclusive)")
	}
	if *allFlag && (*filesFlag != "" || *excludeFlag != "") {
		fatal("Cannot combine --all with --files or --exclude (different transfers have different file lists)")
	}

	client := requireClient()

	// Parse file selection (1-indexed CLI -> 0-indexed API).
	// R7-F11: auto-detect indices vs patterns. Patterns need the file list
	// from the pending transfer, so we fetch it when patterns are detected.
	var filesAPI, excludeAPI []int
	if *filesFlag != "" {
		filesAPI = resolveFileSelector(client, remaining[0], *filesFlag, "files")
	}
	if *excludeFlag != "" {
		excludeAPI = resolveFileSelector(client, remaining[0], *excludeFlag, "exclude")
	}

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
			if err := client.TransferAccept(p.ID, *destFlag, nil, nil); err != nil {
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
	if err := client.TransferAccept(id, *destFlag, filesAPI, excludeAPI); err != nil {
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
		if filesAPI != nil {
			fmt.Printf(" transfer %s (%d files selected)\n", id, len(filesAPI))
		} else if excludeAPI != nil {
			fmt.Printf(" transfer %s (%d files excluded)\n", id, len(excludeAPI))
		} else {
			fmt.Printf(" transfer %s\n", id)
		}
	}
}

// selectiveHintIndices returns example --files and --exclude values
// adapted to the actual file count (issue 9: avoid showing out-of-range indices).
func selectiveHintIndices(fileCount int) (filesHint, excludeHint string) {
	switch {
	case fileCount <= 1:
		return "1", "1"
	case fileCount == 2:
		return "1", "2"
	case fileCount == 3:
		return "1,3", "2"
	default:
		return fmt.Sprintf("1,3,%d", fileCount), "2,4"
	}
}

// parseIndexList parses a comma-separated list of 1-indexed integers and ranges.
// Supports: "1,3,5", "1-5,10", "1-5,10,15-20". Returns 1-indexed values.
// R8-F1: validates all edge cases. R8-F8: atomic failure (no partial results).
func parseIndexList(s string) ([]int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("empty index list")
	}

	parts := strings.Split(s, ",")
	var result []int
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}

		if strings.Contains(p, "-") {
			// Range: "1-5"
			dashIdx := strings.Index(p, "-")
			if dashIdx == 0 {
				return nil, fmt.Errorf("invalid range %q: missing start value", p)
			}
			startStr := strings.TrimSpace(p[:dashIdx])
			endStr := strings.TrimSpace(p[dashIdx+1:])
			if endStr == "" {
				return nil, fmt.Errorf("invalid range %q: missing end value", p)
			}
			start, err := strconv.Atoi(startStr)
			if err != nil {
				return nil, fmt.Errorf("invalid range start %q: %v", startStr, err)
			}
			end, err := strconv.Atoi(endStr)
			if err != nil {
				return nil, fmt.Errorf("invalid range end %q: %v", endStr, err)
			}
			if start < 1 {
				return nil, fmt.Errorf("file index %d: must be >= 1 (1-indexed)", start)
			}
			if start > end {
				return nil, fmt.Errorf("invalid range: start %d > end %d", start, end)
			}
			if end-start+1 > maxFileCount {
				return nil, fmt.Errorf("range %d-%d too large (max %d files)", start, end, maxFileCount)
			}
			for i := start; i <= end; i++ {
				result = append(result, i)
			}
		} else {
			// Single index: "3"
			v, err := strconv.Atoi(p)
			if err != nil {
				return nil, fmt.Errorf("invalid file index %q: must be a number (1-indexed)", p)
			}
			if v < 1 {
				return nil, fmt.Errorf("file index %d: must be >= 1 (1-indexed)", v)
			}
			result = append(result, v)
		}
	}

	if len(result) == 0 {
		return nil, fmt.Errorf("no file indices specified")
	}
	return result, nil
}

// cliToAPIIndices converts 1-indexed CLI indices to 0-indexed API indices.
// Deduplicates via set semantics (F6).
func cliToAPIIndices(cliIndices []int) []int {
	seen := make(map[int]bool, len(cliIndices))
	result := make([]int, 0, len(cliIndices))
	for _, v := range cliIndices {
		apiIdx := v - 1 // 1-indexed -> 0-indexed
		if !seen[apiIdx] {
			seen[apiIdx] = true
			result = append(result, apiIdx)
		}
	}
	return result
}

// resolveFileSelector parses a --files/--exclude flag value into 0-indexed API indices.
// Auto-detects indices vs patterns (R7-F11). For patterns, fetches the pending
// transfer's file list from the daemon to resolve against. flagName is "files" or
// "exclude" for error messages.
func resolveFileSelector(client *daemonClient, transferID, selector, flagName string) []int {
	if isPatternSelector(selector) {
		pending, pendingErr := client.TransferPending()
		if pendingErr != nil {
			fatal("Cannot fetch file list for pattern matching: %v", pendingErr)
		}
		var fileList []PendingFileInfo
		for _, p := range pending {
			if p.ID == transferID || strings.HasPrefix(p.ID, transferID) {
				fileList = p.Files
				break
			}
		}
		if len(fileList) == 0 {
			fatal("Transfer %q has no file list for pattern matching. Use numeric indices instead.", transferID)
		}
		resolved, resolveErr := resolvePatternSelector(selector, fileList)
		if resolveErr != nil {
			fatal("Invalid --%s pattern: %v", flagName, resolveErr)
		}
		return resolved
	}
	parsed, err := parseIndexList(selector)
	if err != nil {
		fatal("Invalid --%s: %v", flagName, err)
	}
	return cliToAPIIndices(parsed)
}

// isPatternSelector returns true if the selector string contains glob characters
// and should be interpreted as file path patterns rather than numeric indices.
// R7-F11: auto-detect indices vs patterns.
func isPatternSelector(s string) bool {
	return strings.ContainsAny(s, "*?[")
}

// resolvePatternSelector resolves glob/literal patterns against a file list,
// returning 0-indexed file indices that match. R7-F11.
// Patterns are comma-separated. Each pattern is matched against file paths
// using filepath.Match (glob) or exact string prefix match (literal/directory).
// SEC-F5: patterns are matched against sanitized paths from the wire header.
func resolvePatternSelector(selector string, files []PendingFileInfo) ([]int, error) {
	patterns := strings.Split(selector, ",")
	matched := make(map[int]bool)

	for _, pat := range patterns {
		pat = strings.TrimSpace(pat)
		if pat == "" {
			continue
		}

		anyMatch := false
		for _, f := range files {
			// Try glob match first.
			if isPatternSelector(pat) {
				// Match against full path and base name.
				if ok, _ := filepath.Match(pat, f.Path); ok {
					matched[f.Index] = true
					anyMatch = true
					continue
				}
				if ok, _ := filepath.Match(pat, filepath.Base(f.Path)); ok {
					matched[f.Index] = true
					anyMatch = true
					continue
				}
			} else {
				// Literal match: exact path, base name, or directory prefix.
				if f.Path == pat || filepath.Base(f.Path) == pat {
					matched[f.Index] = true
					anyMatch = true
					continue
				}
				// Directory prefix: "subdir/" matches "subdir/file.txt"
				dirPat := strings.TrimSuffix(pat, "/") + "/"
				if strings.HasPrefix(f.Path, dirPat) {
					matched[f.Index] = true
					anyMatch = true
				}
			}
		}
		if !anyMatch {
			return nil, fmt.Errorf("pattern %q matched no files", pat)
		}
	}

	if len(matched) == 0 {
		return nil, fmt.Errorf("no patterns matched any files")
	}

	result := make([]int, 0, len(matched))
	for idx := range matched {
		result = append(result, idx)
	}
	sort.Ints(result)
	return result, nil
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

	// R8-F2: reject applies to the entire transfer. Selective control is through accept.
	for _, arg := range remaining {
		if arg == "--files" || arg == "--exclude" {
			fatal("Reject applies to the entire transfer. To accept specific files, use:\n  shurli accept <id> --exclude <indices>")
		}
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
