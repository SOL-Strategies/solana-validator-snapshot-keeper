package keeper

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/charmbracelet/log"

	"github.com/sol-strategies/solana-validator-snapshot-keeper/internal/config"
	"github.com/sol-strategies/solana-validator-snapshot-keeper/internal/discovery"
	"github.com/sol-strategies/solana-validator-snapshot-keeper/internal/downloader"
	"github.com/sol-strategies/solana-validator-snapshot-keeper/internal/hooks"
	"github.com/sol-strategies/solana-validator-snapshot-keeper/internal/pruner"
	"github.com/sol-strategies/solana-validator-snapshot-keeper/internal/rpc"
)

func logger() *log.Logger { return log.Default().WithPrefix("keeper") }

const slotDuration = 400 * time.Millisecond

func slotsToTime(slots uint64) string {
	d := (time.Duration(slots) * slotDuration).Round(time.Second)
	return d.String()
}

// downloadMode determines what kind of snapshot to download.
type downloadMode string

const (
	modeSkip        downloadMode = "skip"
	modeIncremental downloadMode = "incremental"
	modeFull        downloadMode = "full"
)

// Keeper orchestrates the snapshot keeping process.
type Keeper struct {
	cfg        *config.Config
	localRPC   *rpc.Client
	clusterRPC *rpc.Client
}

// New creates a new Keeper.
func New(cfg *config.Config) *Keeper {
	return &Keeper{
		cfg:        cfg,
		localRPC:   rpc.NewClient(cfg.Validator.RPCURL),
		clusterRPC: rpc.NewClient(cfg.Cluster.EffectiveRPCURL()),
	}
}

// Run executes one cycle of the snapshot keeper.
func (k *Keeper) Run(ctx context.Context) error {
	// Step 1: Check identity
	role, identity, err := k.checkRole(ctx)
	if err != nil {
		return fmt.Errorf("checking role: %w", err)
	}
	if role == "active" {
		logger().Info("validator is active, skipping snapshot download", "identity", identity)
		return nil
	}
	if identity != "" {
		logger().Info(fmt.Sprintf("validator is %s", role), "identity", identity)
	} else {
		logger().Info("validator is %s", role)
	}

	// Step 2: Assess local snapshot freshness
	currentSlot, err := k.clusterRPC.GetSlot(ctx)
	if err != nil {
		return fmt.Errorf("getting current slot: %w", err)
	}

	mode, localFullSlot, err := k.assessFreshness(currentSlot)
	if err != nil {
		return fmt.Errorf("assessing freshness: %w", err)
	}

	if mode == modeSkip {
		logger().Info("local snapshots within configured freshness thresholds - nothing to do")
		return nil
	}

	logger().Debug(fmt.Sprintf("%s download mode determined", mode), "current_slot", currentSlot)

	// Step 3: Discover nodes
	clusterNodes, err := k.clusterRPC.GetClusterNodes(ctx)
	if err != nil {
		return k.runFailureHooks(ctx, role, fmt.Errorf("getting cluster nodes: %w", err))
	}

	baseOpts := discovery.Options{
		MaxLatency:          k.cfg.Snapshots.Discovery.Probe.MaxLatencyDuration,
		MaxSnapshotAgeSlots: k.cfg.Snapshots.Age.Remote.MaxSlots,
		ProbeConcurrency:    k.cfg.Snapshots.Discovery.Probe.Concurrency,
		SortOrder:           k.cfg.Snapshots.Discovery.Candidates.SortOrder,
	}

	var candidates []discovery.SnapshotNode

	if mode == modeIncremental {
		incOpts := baseOpts
		incOpts.MinSuitable = k.cfg.Snapshots.Discovery.Candidates.MinSuitableIncremental
		candidates = discovery.DiscoverIncrementalForBase(ctx, clusterNodes, currentSlot, localFullSlot, incOpts)
		if len(candidates) == 0 {
			logger().Info("no matching incrementals found, falling back to full download")
			mode = modeFull
		}
	}

	// Step 4: Download with speed testing
	dlOpts := downloader.Options{
		MinDownloadSpeedBytes: k.cfg.Snapshots.Download.MinSpeedBytes,
		MinSpeedCheckDelay:    k.cfg.Snapshots.Download.MinSpeedCheckDelayDur,
		DownloadConnections:   k.cfg.Snapshots.Download.Connections,
		DownloadTimeout:       k.cfg.Snapshots.Download.TimeoutDur,
	}

	// Create a cancellable context for mid-download identity monitoring
	downloadCtx, cancelDownload := context.WithCancel(ctx)
	defer cancelDownload()

	// Monitor identity during download
	go k.monitorIdentity(downloadCtx, cancelDownload)

	var result *downloader.Result
	var selectedNode discovery.SnapshotNode
	pairedDone := false

	if mode == modeFull {
		// Try paired discovery first (full + incremental from same node)
		pairedResult, pairedNode, pairedErr := k.tryPairedFullDownload(downloadCtx, clusterNodes, currentSlot, localFullSlot, baseOpts, dlOpts)
		if pairedErr == nil {
			result = pairedResult
			selectedNode = pairedNode
			pairedDone = true
		} else {
			logger().Info("paired discovery failed, falling back to full-only discovery", "error", pairedErr)
		}
	}

	if !pairedDone {
		if mode == modeFull {
			fullOpts := baseOpts
			fullOpts.MinSuitable = k.cfg.Snapshots.Discovery.Candidates.MinSuitableFull
			candidates = discovery.DiscoverNodes(ctx, clusterNodes, currentSlot, discovery.SnapshotTypeFull, fullOpts)
		}

		if len(candidates) == 0 {
			return k.runFailureHooks(ctx, role, fmt.Errorf("no suitable snapshot nodes found"))
		}

		for i, candidate := range candidates {
			logger().Info(fmt.Sprintf("attempting candidate %d of %d", i+1, len(candidates)),
				"rpc_url", candidate.RPCURL,
				"slot", candidate.Slot,
				"latency", candidate.Latency,
			)

			result, err = downloader.Download(downloadCtx, candidate.SnapshotURL, k.cfg.Snapshots.Directory, candidate.Filename, dlOpts)
			if err != nil {
				logger().Warn("candidate failed", "node", candidate.RPCURL, "error", err)
				continue
			}

			selectedNode = candidate
			break
		}

		if result == nil {
			return k.runFailureHooks(ctx, role, fmt.Errorf("all %d candidates failed", len(candidates)))
		}
	}

	logger().Info(fmt.Sprintf("%s snapshot downloaded successfully", mode),
		"file", filepath.Join(k.cfg.Snapshots.Directory, selectedNode.Filename),
	)

	// Step 5: If we downloaded a full (non-paired), try to get a matching incremental
	if mode == modeFull && !pairedDone {
		incOpts := baseOpts
		incOpts.MinSuitable = k.cfg.Snapshots.Discovery.Candidates.MinSuitableIncremental
		k.tryDownloadIncremental(ctx, clusterNodes, currentSlot, selectedNode.Slot, incOpts, dlOpts)
	}

	// Log freshness after all downloads
	if localSnaps, err := pruner.GetLocalSnapshots(k.cfg.Snapshots.Directory); err == nil && len(localSnaps) > 0 {
		newestSlot := pruner.NewestSlot(localSnaps)
		if currentSlot > newestSlot {
			behindSlots := currentSlot - newestSlot
			logger().Info(fmt.Sprintf("latest snapshot behind network by %d slots (%s), target is %d slots (%s)", behindSlots, slotsToTime(behindSlots), uint64(k.cfg.Snapshots.Age.Local.MaxIncrementalSlots), slotsToTime(uint64(k.cfg.Snapshots.Age.Local.MaxIncrementalSlots))))
		}
	}

	// Step 6: Prune old snapshots
	if err := pruner.Prune(k.cfg.Snapshots.Directory); err != nil {
		logger().Error("pruning failed", "error", err)
	}

	// Step 7: Run success hooks
	hookData := hooks.TemplateData{
		SnapshotSlot:    fmt.Sprintf("%d", selectedNode.Slot),
		SnapshotType:    string(mode),
		SourceNode:      selectedNode.RPCURL,
		DownloadTimeSec: int(result.DurationSecs),
		DownloadSizeMB:  int(result.Bytes / (1024 * 1024)),
		SnapshotPath:    result.FilePath,
		ClusterName:     k.cfg.Cluster.Name,
		ValidatorRole:   role,
	}

	if err := hooks.RunHooks(ctx, k.cfg.Hooks.OnSuccess, hookData); err != nil {
		logger().Error("success hooks failed", "error", err)
	}

	return nil
}

func (k *Keeper) checkRole(ctx context.Context) (string, string, error) {
	identity, err := k.localRPC.GetIdentity(ctx)
	if err != nil {
		logger().Warn("local RPC unreachable, assuming validator is down", "error", err)
		return "unknown", "", nil
	}
	if identity == k.cfg.Validator.ActiveIdentityPubkey {
		return "active", identity, nil
	}
	return "passive", identity, nil
}

func (k *Keeper) assessFreshness(currentSlot uint64) (downloadMode, uint64, error) {
	snapshots, err := pruner.GetLocalSnapshots(k.cfg.Snapshots.Directory)
	if err != nil {
		return modeFull, 0, nil // if we can't read, just do a full download
	}

	if len(snapshots) == 0 {
		logger().Info("no local snapshots found")
		return modeFull, 0, nil
	}

	newestSlot := pruner.NewestSlot(snapshots)
	newestFull := pruner.NewestFullSnapshot(snapshots)

	if newestSlot >= currentSlot {
		logger().Info("local snapshot is at or ahead of current slot", "local", newestSlot, "current", currentSlot)
		return modeSkip, 0, nil
	}

	age := currentSlot - newestSlot
	skipThreshold := uint64(k.cfg.Snapshots.Age.Local.MaxIncrementalSlots)
	logger().Info(fmt.Sprintf("local snapshot behind network by %d slots (%s), target is %d slots (%s)", age, slotsToTime(age), skipThreshold, slotsToTime(skipThreshold)))

	if age <= skipThreshold {
		return modeSkip, 0, nil
	}

	// If we have a local full, try incremental first — Run() handles fallback to paired/full
	if newestFull != nil {
		fullAge := currentSlot - newestFull.Slot
		logger().Info(fmt.Sprintf("local full snapshot behind network by %d slots (%s) - attempting incremental download", fullAge, slotsToTime(fullAge)))
		return modeIncremental, newestFull.Slot, nil
	}

	return modeFull, 0, nil
}

func (k *Keeper) monitorIdentity(ctx context.Context, cancel context.CancelFunc) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			identity, err := k.localRPC.GetIdentity(ctx)
			if err != nil {
				continue // RPC might be temporarily unavailable
			}
			if identity == k.cfg.Validator.ActiveIdentityPubkey {
				logger().Warn("validator became active during download, aborting")
				cancel()
				return
			}
		}
	}
}

func (k *Keeper) tryPairedFullDownload(ctx context.Context, clusterNodes []rpc.ClusterNode, currentSlot uint64, localFullSlot uint64, opts discovery.Options, dlOpts downloader.Options) (*downloader.Result, discovery.SnapshotNode, error) {
	pairedOpts := opts
	pairedOpts.MinSuitable = k.cfg.Snapshots.Discovery.Candidates.MinSuitableFull

	paired := discovery.DiscoverPairedNodes(ctx, clusterNodes, currentSlot, pairedOpts)
	if len(paired) == 0 {
		return nil, discovery.SnapshotNode{}, fmt.Errorf("no paired snapshot nodes found")
	}

	for i, candidate := range paired {
		// Skip candidates whose full is older than or equal to what we already have locally
		candidateString := fmt.Sprintf("paired candidate %d of %d", i+1, len(paired))
		if localFullSlot > 0 && candidate.Full.Slot <= localFullSlot {
			logger().Info(fmt.Sprintf("skipping %s - local slot %d (us) >= remote slot %d (them)", candidateString, localFullSlot, candidate.Full.Slot))
			continue
		}

		logger().Info(fmt.Sprintf("trying %s", candidateString),
			"rpc_url", candidate.Full.RPCURL,
			"full_slot", candidate.Full.Slot,
			"incremental_slot", candidate.Incremental.Slot, "latency", candidate.Full.Latency,
		)

		// Download full snapshot
		fullResult, err := downloader.Download(ctx, candidate.Full.SnapshotURL, k.cfg.Snapshots.Directory, candidate.Full.Filename, dlOpts)
		if err != nil {
			logger().Warn(fmt.Sprintf("%s full download failed", candidateString), "error", err)
			continue
		}

		logger().Info(fmt.Sprintf("%s full snapshot downloaded", candidateString),
			"slot", candidate.Full.Slot,
			"size", formatBytes(fullResult.Bytes),
		)

		// Download incremental snapshot from the same node
		_, incrErr := downloader.Download(ctx, candidate.Incremental.SnapshotURL, k.cfg.Snapshots.Directory, candidate.Incremental.Filename, dlOpts)
		if incrErr != nil {
			logger().Warn(fmt.Sprintf("%s incremental download failed, full snapshot still usable", candidateString),
				"rpc_url", candidate.Incremental.RPCURL, "error", incrErr)
		} else {
			logger().Info(fmt.Sprintf("%s incremental snapshot downloaded", candidateString),
				"slot", candidate.Incremental.Slot,
				"base_slot", candidate.Incremental.BaseSlot,
			)
		}

		return fullResult, candidate.Full, nil
	}

	return nil, discovery.SnapshotNode{}, fmt.Errorf("all %d paired candidates failed", len(paired))
}

func (k *Keeper) tryDownloadIncremental(ctx context.Context, clusterNodes []rpc.ClusterNode, currentSlot uint64, baseSlot uint64, discoveryOpts discovery.Options, dlOpts downloader.Options) {
	logger().Info("looking for incremental snapshot", "base_slot", baseSlot)

	candidates := discovery.DiscoverIncrementalForBase(ctx, clusterNodes, currentSlot, baseSlot, discoveryOpts)
	if len(candidates) == 0 {
		logger().Info("no matching incremental snapshots available")
		return
	}

	maxCandidates := 3 // don't try too many for the optional incremental
	if maxCandidates > len(candidates) {
		maxCandidates = len(candidates)
	}

	for i := 0; i < maxCandidates; i++ {
		candidate := candidates[i]
		_, err := downloader.Download(ctx, candidate.SnapshotURL, k.cfg.Snapshots.Directory, candidate.Filename, dlOpts)
		if err != nil {
			logger().Warn("incremental download failed", "node", candidate.RPCURL, "error", err)
			continue
		}
		logger().Info("incremental snapshot downloaded", "slot", candidate.Slot, "base_slot", candidate.BaseSlot)
		return
	}

	logger().Info("could not download incremental snapshot, full snapshot is still available")
}

func (k *Keeper) runFailureHooks(ctx context.Context, role string, originalErr error) error {
	logger().Error("snapshot cycle failed", "error", originalErr)

	hookData := hooks.TemplateData{
		ClusterName:   k.cfg.Cluster.Name,
		ValidatorRole: role,
		Error:         originalErr.Error(),
	}

	if err := hooks.RunHooks(ctx, k.cfg.Hooks.OnFailure, hookData); err != nil {
		logger().Error("failure hooks failed", "error", err)
	}

	return originalErr
}

func formatBytes(b int64) string {
	switch {
	case b >= 1024*1024*1024:
		return fmt.Sprintf("%.1f GB", float64(b)/(1024*1024*1024))
	case b >= 1024*1024:
		return fmt.Sprintf("%.1f MB", float64(b)/(1024*1024))
	case b >= 1024:
		return fmt.Sprintf("%.1f KB", float64(b)/1024)
	default:
		return fmt.Sprintf("%d B", b)
	}
}
