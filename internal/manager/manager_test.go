package manager

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sol-strategies/solana-validator-snapshot-keeper/internal/config"
)

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()
	return &config.Config{
		Validator: config.Validator{
			RPCURL:              "http://127.0.0.1:1",
			ActiveIdentityPubkey: "test",
		},
		Cluster: config.Cluster{
			Name:   "testnet",
			RPCURL: "http://127.0.0.1:1",
		},
		Snapshots: config.Snapshots{
			Directory: dir,
			Discovery: config.Discovery{
				Candidates: config.DiscoveryCandidates{MinSuitableFull: 3, MinSuitableIncremental: 5, SortOrder: "latency"},
				Probe:      config.DiscoveryProbe{MaxLatency: "5s", MaxLatencyDuration: 5 * time.Second, Concurrency: 10},
			},
			Download: config.SnapshotsDownload{
				Connections: 1,
			},
			Age: config.SnapshotsAge{
				Remote: config.SnapshotsRemoteAge{MaxSlots: 1300},
				Local:  config.SnapshotsLocalAge{MaxIncrementalSlots: 1300},
			},
		},
	}
}

func TestAcquireLock_NewLock(t *testing.T) {
	cfg := testConfig(t)
	m := &Manager{config: cfg}

	if err := m.acquireLock(); err != nil {
		t.Fatal(err)
	}
	defer m.releaseLock()

	// Verify lock file exists
	lockPath := filepath.Join(cfg.Snapshots.Directory, lockFilename)
	data, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}

	var info lockInfo
	if err := json.Unmarshal(data, &info); err != nil {
		t.Fatal(err)
	}
	if info.PID != os.Getpid() {
		t.Errorf("expected PID %d, got %d", os.Getpid(), info.PID)
	}
}

func TestAcquireLock_AlreadyLocked(t *testing.T) {
	cfg := testConfig(t)
	m := &Manager{config: cfg}

	if err := m.acquireLock(); err != nil {
		t.Fatal(err)
	}
	defer m.releaseLock()

	// Second acquire should fail (same PID = still alive)
	m2 := &Manager{config: cfg}
	err := m2.acquireLock()
	if err == nil {
		t.Error("expected error for duplicate lock")
	}
}

func TestAcquireLock_StaleLock(t *testing.T) {
	cfg := testConfig(t)

	// Write a lock file with a dead PID
	lockPath := filepath.Join(cfg.Snapshots.Directory, lockFilename)
	info := lockInfo{
		PID:       999999999, // almost certainly not running
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}
	data, _ := json.Marshal(info)
	os.WriteFile(lockPath, data, 0644)

	m := &Manager{config: cfg}
	err := m.acquireLock()
	if err != nil {
		t.Fatalf("should override stale lock, got: %v", err)
	}
	defer m.releaseLock()
}

func TestReleaseLock(t *testing.T) {
	cfg := testConfig(t)
	m := &Manager{config: cfg}

	m.acquireLock()
	m.releaseLock()

	lockPath := filepath.Join(cfg.Snapshots.Directory, lockFilename)
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Error("lock file should be removed after release")
	}
}

func TestCalculateNextBoundary(t *testing.T) {
	tests := []struct {
		name     string
		now      time.Time
		interval time.Duration
	}{
		{
			name:     "10 minute interval",
			now:      time.Date(2024, 1, 15, 10, 53, 0, 0, time.UTC),
			interval: 10 * time.Minute,
		},
		{
			name:     "1 hour interval",
			now:      time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
			interval: 1 * time.Hour,
		},
		{
			name:     "4 hour interval",
			now:      time.Date(2024, 1, 15, 9, 0, 0, 0, time.UTC),
			interval: 4 * time.Hour,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next := calculateNextBoundary(tt.now, tt.interval)
			if !next.After(tt.now) {
				t.Errorf("next boundary %v should be after now %v", next, tt.now)
			}
			// Should be aligned to interval
			midnight := time.Date(tt.now.Year(), tt.now.Month(), tt.now.Day(), 0, 0, 0, 0, tt.now.Location())
			elapsed := next.Sub(midnight)
			if elapsed%tt.interval != 0 {
				t.Errorf("next boundary not aligned: elapsed=%v interval=%v", elapsed, tt.interval)
			}
		})
	}
}

func TestNew(t *testing.T) {
	cfg := testConfig(t)
	m := New(cfg)
	if m == nil {
		t.Fatal("expected non-nil manager")
	}
}
