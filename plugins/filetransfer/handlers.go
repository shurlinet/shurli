package filetransfer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/shurlinet/shurli/internal/daemon"
	"github.com/shurlinet/shurli/pkg/p2pnet"
)

func (p *FileTransferPlugin) handleShareList(w http.ResponseWriter, r *http.Request) {
	p.mu.RLock()
	reg := p.shareRegistry
	pnet := p.network
	p.mu.RUnlock()
	if reg == nil {
		daemon.RespondJSON(w, http.StatusOK, []ShareInfo{})
		return
	}

	shares := reg.ListShares(nil)
	infos := make([]ShareInfo, 0, len(shares))
	for _, entry := range shares {
		info := ShareInfo{
			Path:       entry.Path,
			Persistent: entry.Persistent,
			IsDir:      entry.IsDir,
			SharedAt:   entry.SharedAt.Format(time.RFC3339),
		}
		if entry.Peers != nil {
			for pid := range entry.Peers {
				info.Peers = append(info.Peers, pid.String())
			}
		}
		infos = append(infos, info)
	}

	if daemon.WantsText(r) {
		// Build reverse lookup: peer ID -> name.
		// When multiple names exist for the same peer, prefer the shorter one.
		reverseNames := make(map[string]string)
		if pnet != nil {
			for name, pid := range pnet.ListNames() {
				key := pid.String()
				if existing, ok := reverseNames[key]; !ok || len(name) < len(existing) {
					reverseNames[key] = name
				}
			}
		}

		var sb strings.Builder
		for _, info := range infos {
			kind := "file"
			if info.IsDir {
				kind = "dir "
			}
			peerStr := "all"
			if len(info.Peers) > 0 {
				names := make([]string, 0, len(info.Peers))
				for _, pidStr := range info.Peers {
					if name, ok := reverseNames[pidStr]; ok {
						names = append(names, name)
					} else {
						// Truncate peer ID for readability.
						if len(pidStr) > 16 {
							pidStr = pidStr[:16] + "..."
						}
						names = append(names, pidStr)
					}
				}
				sort.Strings(names)
				peerStr = strings.Join(names, ", ")
			}
			fmt.Fprintf(&sb, "%s\t%s\t%s\n", kind, info.Path, peerStr)
		}
		daemon.RespondText(w, http.StatusOK, sb.String())
		return
	}

	daemon.RespondJSON(w, http.StatusOK, infos)
}

func (p *FileTransferPlugin) handleShareAdd(w http.ResponseWriter, r *http.Request) {
	var req ShareRequest
	if err := daemon.ParseJSON(r, &req); err != nil {
		daemon.RespondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Path == "" {
		daemon.RespondError(w, http.StatusBadRequest, "path is required")
		return
	}

	p.mu.RLock()
	reg := p.shareRegistry
	pnet := p.network
	cfg := p.config
	p.mu.RUnlock()

	if reg == nil {
		daemon.RespondError(w, http.StatusServiceUnavailable, "file sharing is not enabled")
		return
	}
	if pnet == nil {
		daemon.RespondError(w, http.StatusServiceUnavailable, "network not available")
		return
	}
	var peerIDs []peer.ID
	for _, pidStr := range req.Peers {
		if resolved, err := pnet.ResolveName(pidStr); err == nil {
			peerIDs = append(peerIDs, resolved)
			continue
		}
		pid, err := peer.Decode(pidStr)
		if err != nil {
			daemon.RespondError(w, http.StatusBadRequest, fmt.Sprintf("invalid peer ID or name %q: %v", pidStr, err))
			return
		}
		peerIDs = append(peerIDs, pid)
	}

	// Resolve persistence: explicit flag wins, otherwise use config default.
	persistent := cfg.defaultPersistent()
	if req.Persistent != nil {
		persistent = *req.Persistent
	}

	if err := reg.Share(req.Path, peerIDs, persistent); err != nil {
		daemon.RespondError(w, http.StatusBadRequest, err.Error())
		return
	}

	// E1-design mitigation: warn about peers without data access grants.
	var warnings []string
	for i, pid := range peerIDs {
		if !p.ctx.HasGrant(pid, "file-browse") && !p.ctx.HasGrant(pid, "file-download") {
			name := req.Peers[i]
			warnings = append(warnings, fmt.Sprintf(
				"%s does not have data access. They won't be able to reach this share until granted. "+
					"Run: shurli auth grant %s --duration 1h", name, name))
		}
	}

	slog.Info("path shared via API", "path", req.Path, "peers", len(req.Peers))
	resp := map[string]any{"status": "shared"}
	if len(warnings) > 0 {
		resp["warnings"] = warnings
	}
	daemon.RespondJSON(w, http.StatusOK, resp)
}

func (p *FileTransferPlugin) handleShareRemove(w http.ResponseWriter, r *http.Request) {
	var req UnshareRequest
	if err := daemon.ParseJSON(r, &req); err != nil {
		daemon.RespondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Path == "" {
		daemon.RespondError(w, http.StatusBadRequest, "path is required")
		return
	}

	p.mu.RLock()
	reg := p.shareRegistry
	p.mu.RUnlock()
	if reg == nil {
		daemon.RespondError(w, http.StatusServiceUnavailable, "file sharing is not enabled")
		return
	}

	if err := reg.Unshare(req.Path); err != nil {
		daemon.RespondError(w, http.StatusNotFound, err.Error())
		return
	}

	slog.Info("path unshared via API", "path", req.Path)
	daemon.RespondJSON(w, http.StatusOK, map[string]string{"status": "unshared"})
}

func (p *FileTransferPlugin) handleShareDeny(w http.ResponseWriter, r *http.Request) {
	var req ShareDenyRequest
	if err := daemon.ParseJSON(r, &req); err != nil {
		daemon.RespondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Path == "" || req.Peer == "" {
		daemon.RespondError(w, http.StatusBadRequest, "path and peer are required")
		return
	}

	p.mu.RLock()
	reg := p.shareRegistry
	pnet := p.network
	p.mu.RUnlock()

	if reg == nil {
		daemon.RespondError(w, http.StatusServiceUnavailable, "file sharing is not enabled")
		return
	}
	if pnet == nil {
		daemon.RespondError(w, http.StatusServiceUnavailable, "network not available")
		return
	}

	// Resolve peer name to ID.
	peerID, err := pnet.ResolveName(req.Peer)
	if err != nil {
		peerID, err = peer.Decode(req.Peer)
		if err != nil {
			daemon.RespondError(w, http.StatusBadRequest, fmt.Sprintf("invalid peer name or ID %q", req.Peer))
			return
		}
	}

	if err := reg.DenyPeer(req.Path, peerID); err != nil {
		daemon.RespondError(w, http.StatusBadRequest, err.Error())
		return
	}

	slog.Info("peer denied from share via API", "path", req.Path, "peer", req.Peer)
	daemon.RespondJSON(w, http.StatusOK, map[string]string{"status": "denied"})
}

func (p *FileTransferPlugin) handleBrowse(w http.ResponseWriter, r *http.Request) {
	var req BrowseRequest
	if err := daemon.ParseJSON(r, &req); err != nil {
		daemon.RespondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Peer == "" {
		daemon.RespondError(w, http.StatusBadRequest, "peer is required")
		return
	}

	// F5 fix: validate sub_path to prevent path traversal.
	if req.SubPath != "" {
		clean := filepath.Clean(req.SubPath)
		if strings.Contains(clean, "..") || filepath.IsAbs(clean) {
			daemon.RespondError(w, http.StatusBadRequest, "sub_path contains path traversal")
			return
		}
	}

	p.mu.RLock()
	pnet := p.network
	p.mu.RUnlock()

	if pnet == nil {
		daemon.RespondError(w, http.StatusServiceUnavailable, "network not available")
		return
	}

	targetPeerID, err := pnet.ResolveName(req.Peer)
	if err != nil {
		daemon.RespondError(w, http.StatusBadRequest, fmt.Sprintf("cannot resolve peer %q: %v", req.Peer, err))
		return
	}

	if err := p.ctx.ConnectToPeer(r.Context(), targetPeerID); err != nil {
		daemon.RespondError(w, http.StatusBadGateway, fmt.Sprintf("cannot reach peer %q: %s", req.Peer, p2pnet.HumanizeError(err.Error())))
		return
	}

	stream, err := pnet.OpenPluginStream(r.Context(), targetPeerID, "file-browse")
	if err != nil {
		daemon.RespondError(w, http.StatusBadGateway, fmt.Sprintf("cannot open browse stream: %s", p2pnet.HumanizeError(err.Error())))
		return
	}
	defer stream.Close()

	result, err := p2pnet.BrowsePeer(stream, req.SubPath)
	if err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, "stream reset") || strings.Contains(errStr, "stream canceled") {
			daemon.RespondError(w, http.StatusForbidden, "no shares visible to you on this peer")
			return
		}
		daemon.RespondError(w, http.StatusInternalServerError, fmt.Sprintf("browse failed: %v", err))
		return
	}

	if result.Error != "" {
		daemon.RespondError(w, http.StatusForbidden, result.Error)
		return
	}

	if daemon.WantsText(r) {
		var sb strings.Builder
		for _, e := range result.Entries {
			kind := "     "
			if e.IsDir {
				kind = "[dir]"
			}
			// D3 fix: sanitize remote-controlled strings for display (terminal injection).
			displayName := p2pnet.SanitizeDisplayName(e.Name)
			downloadPath := p2pnet.SanitizeDisplayName(e.Path)
			if e.ShareID != "" {
				downloadPath = p2pnet.SanitizeDisplayName(e.ShareID) + "/" + downloadPath
			}
			fmt.Fprintf(&sb, "%s %s\t%s\t%s\n", kind, displayName, humanSize(e.Size), downloadPath)
		}
		daemon.RespondText(w, http.StatusOK, sb.String())
		return
	}

	daemon.RespondJSON(w, http.StatusOK, BrowseResponse{Entries: result.Entries})
}

func (p *FileTransferPlugin) handleDownload(w http.ResponseWriter, r *http.Request) {
	var req DownloadRequest
	if err := daemon.ParseJSON(r, &req); err != nil {
		daemon.RespondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Peer == "" || req.RemotePath == "" {
		daemon.RespondError(w, http.StatusBadRequest, "peer and remote_path are required")
		return
	}

	// F6 fix: sanitize remote_path to prevent path traversal before sending to remote peer.
	cleanRemote := filepath.Clean(req.RemotePath)
	if strings.Contains(cleanRemote, "..") {
		daemon.RespondError(w, http.StatusBadRequest, "remote_path contains path traversal")
		return
	}

	p.mu.RLock()
	ts := p.transferService
	pnet := p.network
	p.mu.RUnlock()

	if ts == nil {
		daemon.RespondError(w, http.StatusServiceUnavailable, "file transfer is not enabled")
		return
	}
	if pnet == nil {
		daemon.RespondError(w, http.StatusServiceUnavailable, "network not available")
		return
	}

	targetPeerID, err := pnet.ResolveName(req.Peer)
	if err != nil {
		daemon.RespondError(w, http.StatusBadRequest, fmt.Sprintf("cannot resolve peer %q: %v", req.Peer, err))
		return
	}

	if err := p.ctx.ConnectToPeer(r.Context(), targetPeerID); err != nil {
		daemon.RespondError(w, http.StatusBadGateway, fmt.Sprintf("cannot reach peer %q: %s", req.Peer, p2pnet.HumanizeError(err.Error())))
		return
	}

	destDir := req.LocalDest
	if destDir == "" {
		destDir = ts.ReceiveDir()
	}

	// P4 fix: path confinement - local_dest must be inside the receive directory.
	receiveDir := ts.ReceiveDir()
	if receiveDir != "" && destDir != receiveDir {
		if !isInsideDir(receiveDir, destDir) {
			daemon.RespondError(w, http.StatusBadRequest, "local_dest must be inside the receive directory")
			return
		}
	}

	// Multi-peer download path.
	if req.MultiPeer && ts.MultiPeerEnabled() && len(req.ExtraPeers) > 0 {
		allPeers := []peer.ID{targetPeerID}
		for _, name := range req.ExtraPeers {
			pid, resolveErr := pnet.ResolveName(name)
			if resolveErr != nil {
				slog.Warn("multi-peer: cannot resolve extra peer", "name", name, "error", resolveErr)
				continue
			}
			if connectErr := p.ctx.ConnectToPeer(r.Context(), pid); connectErr != nil {
				slog.Warn("multi-peer: cannot reach extra peer", "name", name, "error", connectErr)
				continue
			}
			allPeers = append(allPeers, pid)
		}

		if len(allPeers) >= 2 {
			rootHash, probeErr := ts.ProbeRootHash(func() (network.Stream, error) {
				return pnet.OpenPluginStream(r.Context(), targetPeerID, "file-download")
			}, req.RemotePath)
			if probeErr == nil {
				opener := func(pid peer.ID) (network.Stream, error) {
					return pnet.OpenPluginStream(r.Context(), pid, "file-multi-peer")
				}
				progress, dlErr := ts.DownloadMultiPeer(r.Context(), rootHash, allPeers, opener, destDir)
				if dlErr == nil {
					snap := progress.Snapshot()
					slog.Info("multi-peer download started via API",
						"id", snap.ID, "file", snap.Filename, "peers", len(allPeers))
					daemon.RespondJSON(w, http.StatusOK, DownloadResponse{
						TransferID: snap.ID,
						FileName:   snap.Filename,
						FileSize:   snap.Size,
					})
					return
				}
				slog.Warn("multi-peer download failed, falling back to single-peer", "error", dlErr)
			} else {
				slog.Warn("root hash probe failed, falling back to single-peer", "error", probeErr)
			}
		}
	}

	// Single-peer download. Use r.Context() so drain cancellation propagates (F2 fix).
	stream, err := pnet.OpenPluginStream(r.Context(), targetPeerID, "file-download")
	if err != nil {
		daemon.RespondError(w, http.StatusBadGateway, fmt.Sprintf("cannot open download stream: %s", p2pnet.HumanizeError(err.Error())))
		return
	}

	progress, err := ts.ReceiveFrom(stream, req.RemotePath, destDir)
	if err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, "access denied") {
			daemon.RespondError(w, http.StatusForbidden, fmt.Sprintf("download failed: share not found or access denied. Verify the share ID with: shurli browse %s", req.Peer))
			return
		}
		if strings.Contains(errStr, "stream reset") || strings.Contains(errStr, "stream canceled") {
			daemon.RespondError(w, http.StatusForbidden, "download failed: connection reset by remote peer. Check that both peers are online and the share still exists.")
			return
		}
		daemon.RespondError(w, http.StatusInternalServerError, fmt.Sprintf("download failed: %v", err))
		return
	}

	snap := progress.Snapshot()

	// X2 fix: write checkpoint for crash recovery, remove on completion.
	p.mu.RLock()
	cfgDir := p.configDir
	p.mu.RUnlock()
	if cfgDir != "" {
		writeCheckpoint(cfgDir, partialManifest{
			TransferID: snap.ID,
			Filename:   snap.Filename,
			TempPath:   filepath.Join(destDir, snap.Filename+".tmp"),
			PeerID:     req.Peer,
			Size:       snap.Size,
		})
		// X2 fix: remove checkpoint when transfer completes.
		// D1 fix: use activeCtx so goroutine exits on plugin shutdown.
		p.mu.RLock()
		activeCtx := p.activeCtx
		p.mu.RUnlock()
		if activeCtx != nil {
			go func(ctx context.Context, id, dir string, prog *p2pnet.TransferProgress) {
				ticker := time.NewTicker(2 * time.Second)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						if prog.Snapshot().Done {
							removeCheckpoint(dir, id)
							return
						}
					}
				}
			}(activeCtx, snap.ID, cfgDir, progress)
		}
	}

	slog.Info("file download started via API",
		"id", snap.ID, "file", snap.Filename, "peer", req.Peer)

	daemon.RespondJSON(w, http.StatusOK, DownloadResponse{
		TransferID: snap.ID,
		FileName:   snap.Filename,
		FileSize:   snap.Size,
	})
}

func (p *FileTransferPlugin) handleSend(w http.ResponseWriter, r *http.Request) {
	var req SendRequest
	if err := daemon.ParseJSON(r, &req); err != nil {
		daemon.RespondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Path == "" || req.Peer == "" {
		daemon.RespondError(w, http.StatusBadRequest, "path and peer are required")
		return
	}

	// P11 fix: path confinement - reject system paths to prevent file exfiltration.
	cleanPath := filepath.Clean(req.Path)
	if isForbiddenSystemPath(cleanPath) {
		daemon.RespondError(w, http.StatusBadRequest, "path confinement: system paths are not allowed for send")
		return
	}

	p.mu.RLock()
	ts := p.transferService
	pnet := p.network
	p.mu.RUnlock()

	if ts == nil {
		daemon.RespondError(w, http.StatusServiceUnavailable, "file transfer is not enabled")
		return
	}
	if pnet == nil {
		daemon.RespondError(w, http.StatusServiceUnavailable, "network not available")
		return
	}

	targetPeerID, err := pnet.ResolveName(req.Peer)
	if err != nil {
		daemon.RespondError(w, http.StatusBadRequest, fmt.Sprintf("cannot resolve peer %q: %v", req.Peer, err))
		return
	}

	if err := p.ctx.ConnectToPeer(r.Context(), targetPeerID); err != nil {
		daemon.RespondError(w, http.StatusBadGateway, fmt.Sprintf("cannot reach peer %q: %s", req.Peer, p2pnet.HumanizeError(err.Error())))
		return
	}

	// Use plugin's activeCtx for stream opener, not r.Context().
	// r.Context() dies after HTTP response (fire-and-forget send).
	// activeCtx lives until plugin Stop() - cancelled during drain.
	p.mu.RLock()
	ctx := p.activeCtx
	p.mu.RUnlock()
	if ctx == nil {
		daemon.RespondError(w, http.StatusServiceUnavailable, "plugin is shutting down")
		return
	}
	opener := func() (network.Stream, error) {
		return pnet.OpenPluginStream(ctx, targetPeerID, "file-transfer")
	}
	sendOpts := p2pnet.SendOptions{
		NoCompress:   req.NoCompress,
		Streams:      req.Streams,
		StreamOpener: opener,
	}

	priority := p2pnet.PriorityNormal
	switch strings.ToLower(req.Priority) {
	case "low":
		priority = p2pnet.PriorityLow
	case "high":
		priority = p2pnet.PriorityHigh
	}

	progress, err := ts.SubmitSend(req.Path, targetPeerID.String(), priority, opener, sendOpts)
	if err != nil {
		daemon.RespondError(w, http.StatusInternalServerError, fmt.Sprintf("send failed: %v", err))
		return
	}

	snap := progress.Snapshot()
	slog.Info("file transfer queued via API",
		"id", snap.ID, "file", snap.Filename, "peer", req.Peer, "status", snap.Status)

	daemon.RespondJSON(w, http.StatusOK, SendResponse{
		TransferID: snap.ID,
		Filename:   snap.Filename,
		Size:       snap.Size,
		PeerID:     targetPeerID.String(),
	})
}

func (p *FileTransferPlugin) handleTransferList(w http.ResponseWriter, r *http.Request) {
	p.mu.RLock()
	ts := p.transferService
	p.mu.RUnlock()
	if ts == nil {
		daemon.RespondJSON(w, http.StatusOK, []p2pnet.TransferSnapshot{})
		return
	}
	daemon.RespondJSON(w, http.StatusOK, ts.ListTransfers())
}

func (p *FileTransferPlugin) handleTransferHistory(w http.ResponseWriter, r *http.Request) {
	p.mu.RLock()
	ts := p.transferService
	p.mu.RUnlock()
	if ts == nil {
		daemon.RespondJSON(w, http.StatusOK, []p2pnet.TransferEvent{})
		return
	}

	logPath := ts.LogPath()
	if logPath == "" {
		daemon.RespondJSON(w, http.StatusOK, []p2pnet.TransferEvent{})
		return
	}

	maxStr := r.URL.Query().Get("max")
	max := 50
	if maxStr != "" {
		if n, err := fmt.Sscanf(maxStr, "%d", &max); n != 1 || err != nil {
			max = 50
		}
		if max <= 0 {
			max = 50
		}
		if max > 1000 {
			max = 1000
		}
	}

	events, err := p2pnet.ReadTransferEvents(logPath, max)
	if err != nil {
		daemon.RespondError(w, http.StatusInternalServerError, fmt.Sprintf("read transfer log: %v", err))
		return
	}
	if events == nil {
		events = []p2pnet.TransferEvent{}
	}
	daemon.RespondJSON(w, http.StatusOK, events)
}

func (p *FileTransferPlugin) handleTransferPending(w http.ResponseWriter, r *http.Request) {
	p.mu.RLock()
	ts := p.transferService
	p.mu.RUnlock()
	if ts == nil {
		daemon.RespondJSON(w, http.StatusOK, []PendingTransferInfo{})
		return
	}

	pending := ts.ListPending()
	infos := make([]PendingTransferInfo, len(pending))
	for i, pt := range pending {
		infos[i] = PendingTransferInfo{
			ID:       pt.ID,
			Filename: pt.Filename,
			Size:     pt.Size,
			PeerID:   pt.PeerID,
			Time:     pt.Time.Format(time.RFC3339),
		}
	}
	daemon.RespondJSON(w, http.StatusOK, infos)
}

func (p *FileTransferPlugin) handleTransferStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		daemon.RespondError(w, http.StatusBadRequest, "transfer id is required")
		return
	}

	p.mu.RLock()
	ts := p.transferService
	p.mu.RUnlock()
	if ts == nil {
		daemon.RespondError(w, http.StatusNotFound, "file transfer is not enabled")
		return
	}

	progress, ok := ts.GetTransfer(id)
	if !ok {
		daemon.RespondError(w, http.StatusNotFound, fmt.Sprintf("transfer %q not found", id))
		return
	}
	daemon.RespondJSON(w, http.StatusOK, progress.Snapshot())
}

func (p *FileTransferPlugin) handleTransferAccept(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		daemon.RespondError(w, http.StatusBadRequest, "transfer id is required")
		return
	}

	p.mu.RLock()
	ts := p.transferService
	p.mu.RUnlock()
	if ts == nil {
		daemon.RespondError(w, http.StatusServiceUnavailable, "file transfer is not enabled")
		return
	}

	var req TransferAcceptRequest
	if r.Body != nil && r.ContentLength > 0 {
		if err := json.NewDecoder(io.LimitReader(r.Body, daemon.MaxRequestBodySize)).Decode(&req); err != nil {
			daemon.RespondError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}

	if err := ts.AcceptTransfer(id, req.Dest); err != nil {
		daemon.RespondError(w, http.StatusNotFound, err.Error())
		return
	}

	slog.Info("transfer accepted via API", "id", id)
	daemon.RespondJSON(w, http.StatusOK, map[string]string{"status": "accepted"})
}

func (p *FileTransferPlugin) handleTransferReject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		daemon.RespondError(w, http.StatusBadRequest, "transfer id is required")
		return
	}

	p.mu.RLock()
	ts := p.transferService
	p.mu.RUnlock()
	if ts == nil {
		daemon.RespondError(w, http.StatusServiceUnavailable, "file transfer is not enabled")
		return
	}

	var req TransferRejectRequest
	if r.Body != nil && r.ContentLength > 0 {
		if err := json.NewDecoder(io.LimitReader(r.Body, daemon.MaxRequestBodySize)).Decode(&req); err != nil {
			daemon.RespondError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}

	reason := p2pnet.RejectReasonNone
	switch req.Reason {
	case "space":
		reason = p2pnet.RejectReasonSpace
	case "busy":
		reason = p2pnet.RejectReasonBusy
	case "size":
		reason = p2pnet.RejectReasonSize
	}

	if err := ts.RejectTransfer(id, reason); err != nil {
		daemon.RespondError(w, http.StatusNotFound, err.Error())
		return
	}

	slog.Info("transfer rejected via API", "id", id, "reason", req.Reason)
	daemon.RespondJSON(w, http.StatusOK, map[string]string{"status": "rejected"})
}

func (p *FileTransferPlugin) handleTransferCancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		daemon.RespondError(w, http.StatusBadRequest, "transfer id is required")
		return
	}

	p.mu.RLock()
	ts := p.transferService
	p.mu.RUnlock()
	if ts == nil {
		daemon.RespondError(w, http.StatusServiceUnavailable, "file transfer is not enabled")
		return
	}

	if err := ts.CancelTransfer(id); err != nil {
		daemon.RespondError(w, http.StatusNotFound, err.Error())
		return
	}

	slog.Info("transfer cancelled via API", "id", id)
	daemon.RespondJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

func (p *FileTransferPlugin) handleClean(w http.ResponseWriter, r *http.Request) {
	p.mu.RLock()
	ts := p.transferService
	p.mu.RUnlock()
	if ts == nil {
		daemon.RespondError(w, http.StatusServiceUnavailable, "file transfer is not enabled")
		return
	}

	count, bytes := ts.CleanTempFiles()
	slog.Info("temp files cleaned via API", "files", count, "bytes", bytes)
	daemon.RespondJSON(w, http.StatusOK, map[string]any{
		"files_removed": count,
		"bytes_freed":   bytes,
	})
}
