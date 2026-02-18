package discovery

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/log"

	"github.com/sol-strategies/solana-validator-snapshot-keeper/internal/rpc"
)

func logger() *log.Logger { return log.Default().WithPrefix("discovery") }

// SnapshotType indicates whether a snapshot is full or incremental.
type SnapshotType string

const (
	SnapshotTypeFull        SnapshotType = "full"
	SnapshotTypeIncremental SnapshotType = "incremental"
)

// SnapshotNode represents a node that serves snapshots along with probe metadata.
type SnapshotNode struct {
	RPCURL       string
	SnapshotURL  string
	SnapshotType SnapshotType
	Slot         uint64
	BaseSlot     uint64 // only for incremental snapshots
	Filename     string
	Latency      time.Duration
	SlotAge      uint64
}

// Options configures the discovery process.
type Options struct {
	MaxLatency          time.Duration
	MaxSnapshotAgeSlots int
	ProbeConcurrency    int
	SortOrder           string // "latency" or "slot_age"
	MinSuitable         int    // stop probing early once this many suitable nodes found (0 = probe all)
}

var (
	fullSnapshotRe        = regexp.MustCompile(`snapshot-(\d+)-[A-Za-z0-9]+\.tar\.(zst|bz2|gz)`)
	incrementalSnapshotRe = regexp.MustCompile(`incremental-snapshot-(\d+)-(\d+)-[A-Za-z0-9]+\.tar\.(zst|bz2|gz)`)
)

// DiscoverNodes probes cluster nodes for snapshot availability.
// It returns nodes sorted by the configured sort order.
func DiscoverNodes(ctx context.Context, nodes []rpc.ClusterNode, currentSlot uint64, snapshotType SnapshotType, opts Options) []SnapshotNode {
	rpcAddresses := extractRPCAddresses(nodes)
	logger().Info(fmt.Sprintf("probing %d nodes for %s snapshots 👉🍑😭...", len(rpcAddresses), snapshotType))

	start := time.Now()
	results := probeNodes(ctx, rpcAddresses, currentSlot, snapshotType, opts)

	sortNodes(results, opts.SortOrder)

	logger().Info(fmt.Sprintf("probes complete in %s - found %d suitable nodes", time.Since(start), len(results)))
	return results
}

// DiscoverIncrementalForBase discovers incremental snapshots that match a specific base slot.
func DiscoverIncrementalForBase(ctx context.Context, nodes []rpc.ClusterNode, currentSlot uint64, baseSlot uint64, opts Options) []SnapshotNode {
	all := DiscoverNodes(ctx, nodes, currentSlot, SnapshotTypeIncremental, opts)

	var matching []SnapshotNode
	for _, n := range all {
		if n.BaseSlot == baseSlot {
			matching = append(matching, n)
		}
	}

	logger().Info(fmt.Sprintf("found %d of %d candidates with incremental snapshots for base slot %d", len(matching), len(all), baseSlot))
	return matching
}

func extractRPCAddresses(nodes []rpc.ClusterNode) []string {
	var addrs []string
	for _, n := range nodes {
		if n.RPC != nil && *n.RPC != "" {
			addr := *n.RPC
			if !strings.Contains(addr, "://") {
				addr = "http://" + addr
			}
			addrs = append(addrs, addr)
		}
	}
	return addrs
}

type rejectReason int

const (
	rejectHTTPError rejectReason = iota
	rejectLatency
	rejectStatusCode
	rejectParseFail
	rejectTooOld
)

type probeError struct {
	reason     rejectReason
	err        error
	statusCode int    // populated for rejectStatusCode
	slotAge    uint64 // populated for rejectTooOld
}

func (e *probeError) Error() string { return e.err.Error() }
func (e *probeError) Unwrap() error { return e.err }

// rejectionCounters tracks why probe attempts fail, for summary logging.
type rejectionCounters struct {
	mu           sync.Mutex
	httpError    atomic.Int64
	latency      atomic.Int64
	statusCode   atomic.Int64
	statusCodes  map[int]int // actual HTTP status code counts
	parseFail    atomic.Int64
	tooOld       atomic.Int64
	tooOldMinAge atomic.Uint64
	tooOldMaxAge atomic.Uint64
}

func (r *rejectionCounters) record(err error) {
	var pe *probeError
	if !errors.As(err, &pe) {
		r.httpError.Add(1)
		return
	}
	switch pe.reason {
	case rejectHTTPError:
		r.httpError.Add(1)
	case rejectLatency:
		r.latency.Add(1)
	case rejectStatusCode:
		r.statusCode.Add(1)
		r.mu.Lock()
		r.statusCodes[pe.statusCode]++
		r.mu.Unlock()
	case rejectParseFail:
		r.parseFail.Add(1)
	case rejectTooOld:
		r.tooOld.Add(1)
		age := pe.slotAge
		for {
			cur := r.tooOldMinAge.Load()
			if cur != 0 && cur <= age {
				break
			}
			if r.tooOldMinAge.CompareAndSwap(cur, age) {
				break
			}
		}
		for {
			cur := r.tooOldMaxAge.Load()
			if cur >= age {
				break
			}
			if r.tooOldMaxAge.CompareAndSwap(cur, age) {
				break
			}
		}
	}
}

func probeNodes(ctx context.Context, addresses []string, currentSlot uint64, snapshotType SnapshotType, opts Options) []SnapshotNode {
	var (
		mu         sync.Mutex
		results    []SnapshotNode
		sem        = make(chan struct{}, opts.ProbeConcurrency)
		wg         sync.WaitGroup
		probed     atomic.Int64
		suitable   atomic.Int64
		rejections = rejectionCounters{statusCodes: make(map[int]int)}
		earlyOnce  sync.Once
	)

	endpoint := "/snapshot.tar.bz2"
	if snapshotType == SnapshotTypeIncremental {
		endpoint = "/incremental-snapshot.tar.bz2"
	}

	totalAddresses := len(addresses)

	// Progress logging goroutine
	probeCtx, probeCancel := context.WithCancel(ctx)
	defer probeCancel()
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		start := time.Now()
		for {
			select {
			case <-ticker.C:
				logger().Info(fmt.Sprintf("probe progress (%d/%d, %.1f%%)", probed.Load(), totalAddresses, float64(probed.Load())/float64(totalAddresses)*100),
					"suitable", suitable.Load(),
					"elapsed_time", time.Since(start),
				)
			case <-probeCtx.Done():
				return
			}
		}
	}()

	for addrIndex, addr := range addresses {
		wg.Add(1)
		go func(addr string) {
			defer wg.Done()
			defer probed.Add(1)

			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-probeCtx.Done():
				return
			}

			logger().Debug(fmt.Sprintf("probing node %d of %d", addrIndex+1, totalAddresses), "addr", addr, "endpoint", endpoint)
			node, err := probeNode(probeCtx, addr, endpoint, currentSlot, snapshotType, opts)
			if err != nil {
				rejections.record(err)
				logger().Debug(fmt.Sprintf("probing node %d of %d failed", addrIndex+1, totalAddresses), "addr", addr, "endpoint", endpoint, "error", err)
				return
			}

			n := suitable.Add(1)
			mu.Lock()
			results = append(results, *node)
			mu.Unlock()

			if opts.MinSuitable > 0 && int(n) >= opts.MinSuitable {
				earlyOnce.Do(func() {
					logger().Info(fmt.Sprintf("found at least %d (minimum) suitable nodes found - aborting further probes", opts.MinSuitable))
					probeCancel()
				})
			}
		}(addr)
	}

	wg.Wait()
	probeCancel()

	failed := int64(totalAddresses) - int64(len(results))
	if failed > 0 {
		args := []any{
			"http_error", rejections.httpError.Load(),
			"latency", rejections.latency.Load(),
			"status_code", rejections.statusCode.Load(),
			"parse_fail", rejections.parseFail.Load(),
			"too_old", rejections.tooOld.Load(),
		}
		if len(rejections.statusCodes) > 0 {
			args = append(args, "status_codes", fmt.Sprint(rejections.statusCodes))
		}
		if minAge := rejections.tooOldMinAge.Load(); minAge > 0 {
			maxAge := rejections.tooOldMaxAge.Load()
			args = append(args,
				"too_old_min_slots", minAge,
				"too_old_max_slots", maxAge,
				"too_old_min_time", formatSlotDuration(minAge),
				"too_old_max_time", formatSlotDuration(maxAge),
			)
		}
		logger().Debug("probe rejections", args...)
	}

	return results
}

func probeNode(ctx context.Context, addr string, endpoint string, currentSlot uint64, snapshotType SnapshotType, opts Options) (*SnapshotNode, error) {
	url := addr + endpoint

	client := &http.Client{
		Timeout: opts.MaxLatency * 3,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // don't follow redirects
		},
	}

	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, &probeError{reason: rejectHTTPError, err: fmt.Errorf("executing request: %w", err)}
	}
	defer resp.Body.Close()

	latency := time.Since(start)
	if latency > opts.MaxLatency {
		return nil, &probeError{reason: rejectLatency, err: fmt.Errorf("latency %s exceeds max %s", latency, opts.MaxLatency)}
	}

	// The snapshot URL comes from a redirect's Location header, or from the
	// request URL itself on a 200.
	var snapshotFilename string
	switch resp.StatusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect:
		location := resp.Header.Get("Location")
		if location == "" {
			return nil, &probeError{reason: rejectStatusCode, err: fmt.Errorf("redirect with no Location header"), statusCode: resp.StatusCode}
		}
		// Location may be a full URL or just a path
		parts := strings.Split(location, "/")
		snapshotFilename = parts[len(parts)-1]
	case http.StatusOK:
		parts := strings.Split(endpoint, "/")
		snapshotFilename = parts[len(parts)-1]
	default:
		return nil, &probeError{reason: rejectStatusCode, err: fmt.Errorf("unexpected status %d", resp.StatusCode), statusCode: resp.StatusCode}
	}

	node, err := parseSnapshotFilename(snapshotFilename, snapshotType)
	if err != nil {
		return nil, &probeError{reason: rejectParseFail, err: err}
	}

	// Check slot age (skip when MaxSnapshotAgeSlots == 0, used internally by paired probing for full snapshots)
	var slotAge uint64
	if node.Slot > currentSlot {
		return nil, &probeError{reason: rejectTooOld, err: fmt.Errorf("snapshot slot %d is ahead of current slot %d", node.Slot, currentSlot)}
	}
	slotAge = currentSlot - node.Slot
	if opts.MaxSnapshotAgeSlots > 0 && slotAge > uint64(opts.MaxSnapshotAgeSlots) {
		return nil, &probeError{reason: rejectTooOld, err: fmt.Errorf("slot age %d exceeds max %d", slotAge, opts.MaxSnapshotAgeSlots), slotAge: slotAge}
	}

	// Build the download URL from the redirect location
	snapshotURL := addr + "/" + snapshotFilename

	node.RPCURL = addr
	node.SnapshotURL = snapshotURL
	node.Latency = latency
	node.SlotAge = slotAge
	node.Filename = snapshotFilename

	return node, nil
}

func parseSnapshotFilename(filename string, snapshotType SnapshotType) (*SnapshotNode, error) {
	if snapshotType == SnapshotTypeIncremental {
		matches := incrementalSnapshotRe.FindStringSubmatch(filename)
		if matches == nil {
			return nil, fmt.Errorf("filename %q does not match incremental snapshot pattern", filename)
		}
		baseSlot, _ := strconv.ParseUint(matches[1], 10, 64)
		slot, _ := strconv.ParseUint(matches[2], 10, 64)
		return &SnapshotNode{
			SnapshotType: SnapshotTypeIncremental,
			Slot:         slot,
			BaseSlot:     baseSlot,
		}, nil
	}

	matches := fullSnapshotRe.FindStringSubmatch(filename)
	if matches == nil {
		return nil, fmt.Errorf("filename %q does not match full snapshot pattern", filename)
	}
	slot, _ := strconv.ParseUint(matches[1], 10, 64)
	return &SnapshotNode{
		SnapshotType: SnapshotTypeFull,
		Slot:         slot,
	}, nil
}

const slotDuration = 400 * time.Millisecond

func formatSlotDuration(slots uint64) string {
	d := (time.Duration(slots) * slotDuration).Round(time.Second)
	return d.String()
}

func sortNodes(nodes []SnapshotNode, sortOrder string) {
	sort.Slice(nodes, func(i, j int) bool {
		if sortOrder == "slot_age" {
			return nodes[i].SlotAge < nodes[j].SlotAge
		}
		// default: latency
		return nodes[i].Latency < nodes[j].Latency
	})
}

// PairedSnapshotNode represents a node that serves both a full and matching incremental snapshot.
type PairedSnapshotNode struct {
	Full        SnapshotNode
	Incremental SnapshotNode
}

// DiscoverPairedNodes probes cluster nodes for paired full+incremental snapshot availability.
// The full snapshot is not filtered by age — only the incremental must be fresh.
// The incremental's base slot must match the full's slot.
func DiscoverPairedNodes(ctx context.Context, nodes []rpc.ClusterNode, currentSlot uint64, opts Options) []PairedSnapshotNode {
	rpcAddresses := extractRPCAddresses(nodes)
	logger().Info("probing nodes for paired snapshots", "candidates", len(rpcAddresses))

	start := time.Now()
	results := probePairedNodes(ctx, rpcAddresses, currentSlot, opts)

	sortPairedNodes(results, opts.SortOrder)

	logger().Info("paired discovery complete", "suitable", len(results), "elapsed", time.Since(start))
	return results
}

type pairedRejectReason int

const (
	pairedRejectFullFailed pairedRejectReason = iota
	pairedRejectIncrFailed
	pairedRejectBaseSlotMismatch
)

func probePairedNode(ctx context.Context, addr string, currentSlot uint64, opts Options) (*PairedSnapshotNode, pairedRejectReason, error) {
	// Probe full snapshot with no age filter
	fullOpts := opts
	fullOpts.MaxSnapshotAgeSlots = 0
	fullNode, err := probeNode(ctx, addr, "/snapshot.tar.bz2", currentSlot, SnapshotTypeFull, fullOpts)
	if err != nil {
		return nil, pairedRejectFullFailed, fmt.Errorf("full probe: %w", err)
	}

	// Probe incremental snapshot with age filter
	incrNode, err := probeNode(ctx, addr, "/incremental-snapshot.tar.bz2", currentSlot, SnapshotTypeIncremental, opts)
	if err != nil {
		return nil, pairedRejectIncrFailed, fmt.Errorf("incremental probe: %w", err)
	}

	// Validate base slot matches
	if incrNode.BaseSlot != fullNode.Slot {
		return nil, pairedRejectBaseSlotMismatch, fmt.Errorf("base slot mismatch: incremental base %d != full slot %d", incrNode.BaseSlot, fullNode.Slot)
	}

	return &PairedSnapshotNode{Full: *fullNode, Incremental: *incrNode}, 0, nil
}

func probePairedNodes(ctx context.Context, addresses []string, currentSlot uint64, opts Options) []PairedSnapshotNode {
	var (
		mu       sync.Mutex
		results  []PairedSnapshotNode
		sem      = make(chan struct{}, opts.ProbeConcurrency)
		wg       sync.WaitGroup
		probed   atomic.Int64
		suitable atomic.Int64

		fullFailed   atomic.Int64
		incrFailed   atomic.Int64
		baseMismatch atomic.Int64
		earlyOnce    sync.Once
	)

	totalAddresses := len(addresses)

	probeCtx, probeCancel := context.WithCancel(ctx)
	defer probeCancel()
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		start := time.Now()
		for {
			select {
			case <-ticker.C:
				logger().Info(fmt.Sprintf("paired probe progress (%d/%d, %.1f%%)", probed.Load(), totalAddresses, float64(probed.Load())/float64(totalAddresses)*100),
					"suitable", suitable.Load(),
					"elapsed_time", time.Since(start),
				)
			case <-probeCtx.Done():
				return
			}
		}
	}()

	for addrIndex, addr := range addresses {
		wg.Add(1)
		go func(addr string) {
			defer wg.Done()
			defer probed.Add(1)

			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-probeCtx.Done():
				return
			}

			logger().Debug(fmt.Sprintf("probing node %d of %d for paired snapshots", addrIndex+1, totalAddresses), "addr", addr)
			pair, reason, err := probePairedNode(probeCtx, addr, currentSlot, opts)
			if err != nil {
				switch reason {
				case pairedRejectFullFailed:
					fullFailed.Add(1)
				case pairedRejectIncrFailed:
					incrFailed.Add(1)
				case pairedRejectBaseSlotMismatch:
					baseMismatch.Add(1)
				}
				logger().Debug(fmt.Sprintf("paired probe node %d of %d failed", addrIndex+1, totalAddresses), "addr", addr, "error", err)
				return
			}

			n := suitable.Add(1)
			mu.Lock()
			results = append(results, *pair)
			mu.Unlock()

			if opts.MinSuitable > 0 && int(n) >= opts.MinSuitable {
				earlyOnce.Do(func() {
					logger().Info("minimum suitable paired candidates found, stopping probes", "suitable", n, "min_suitable", opts.MinSuitable)
					probeCancel()
				})
			}
		}(addr)
	}

	wg.Wait()
	probeCancel()

	failed := int64(totalAddresses) - int64(len(results))
	if failed > 0 {
		logger().Info("paired probe rejections",
			"full_failed", fullFailed.Load(),
			"incremental_failed", incrFailed.Load(),
			"base_slot_mismatch", baseMismatch.Load(),
		)
	}

	return results
}

func sortPairedNodes(nodes []PairedSnapshotNode, sortOrder string) {
	sort.Slice(nodes, func(i, j int) bool {
		if sortOrder == "slot_age" {
			return nodes[i].Incremental.SlotAge < nodes[j].Incremental.SlotAge
		}
		// default: latency — use combined latency
		return (nodes[i].Full.Latency + nodes[i].Incremental.Latency) < (nodes[j].Full.Latency + nodes[j].Incremental.Latency)
	})
}
