package keeper

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sol-strategies/solana-validator-snapshot-keeper/internal/config"
)

// rpcServer creates a test JSON-RPC server with configurable responses.
func rpcServer(t *testing.T, identity string, slot uint64, nodes []map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			ID     int    `json:"id"`
		}
		json.NewDecoder(r.Body).Decode(&req)

		var result any
		switch req.Method {
		case "getIdentity":
			result = map[string]string{"identity": identity}
		case "getSlot":
			result = slot
		case "getClusterNodes":
			result = nodes
		default:
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		resultJSON, _ := json.Marshal(result)
		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  json.RawMessage(resultJSON),
		}
		json.NewEncoder(w).Encode(resp)
	}))
}

// snapshotServer creates a test server that serves snapshot HEAD redirects and GET data.
func snapshotServer(t *testing.T, fullFilename string, data []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			if strings.Contains(r.URL.Path, "snapshot.tar.bz2") {
				w.Header().Set("Location", "/"+fullFilename)
				w.WriteHeader(http.StatusFound)
				return
			}
			w.WriteHeader(http.StatusNotFound)
			return
		}

		// GET — serve snapshot data
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.WriteHeader(http.StatusOK)
		w.Write(data)
	}))
}

func TestRun_ActiveValidator_Skips(t *testing.T) {
	localRPC := rpcServer(t, "ActivePubkey", 100000, nil)
	defer localRPC.Close()

	snapshotDir := t.TempDir()
	cfg := &config.Config{
		Validator: config.Validator{
			RPCURL:              localRPC.URL,
			ActiveIdentityPubkey: "ActivePubkey",
		},
		Cluster:  config.Cluster{Name: "testnet", RPCURL: localRPC.URL},
		Snapshots: config.Snapshots{Directory: snapshotDir},
	}

	k := New(cfg)
	err := k.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Should have returned early without error (skipping)
}

func TestRun_FreshSnapshots_Skips(t *testing.T) {
	localRPC := rpcServer(t, "PassivePubkey", 100100, nil)
	defer localRPC.Close()

	clusterRPC := rpcServer(t, "", 100100, nil)
	defer clusterRPC.Close()

	snapshotDir := t.TempDir()
	// Create a recent local snapshot
	os.WriteFile(filepath.Join(snapshotDir, "snapshot-100000-HashA.tar.zst"), []byte("data"), 0644)

	cfg := &config.Config{
		Validator: config.Validator{
			RPCURL:              localRPC.URL,
			ActiveIdentityPubkey: "ActivePubkey", // different from current
		},
		Cluster: config.Cluster{Name: "testnet", RPCURL: clusterRPC.URL},
		Snapshots: config.Snapshots{
			Directory: snapshotDir,
			Discovery: config.Discovery{
				Candidates: config.DiscoveryCandidates{SortOrder: "latency"},
				Probe:      config.DiscoveryProbe{MaxLatency: "5s", MaxLatencyDuration: 5 * time.Second, Concurrency: 10},
			},
			Age: config.SnapshotsAge{
				Remote: config.SnapshotsRemoteAge{MaxSlots: 1300},
				Local:  config.SnapshotsLocalAge{MaxIncrementalSlots: 1300},
			},
		},
	}

	k := New(cfg)
	err := k.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Age is 100 slots, well within 1300, should skip
}

func TestRun_RPCUnreachable_Proceeds(t *testing.T) {
	// Local RPC unreachable
	clusterRPC := rpcServer(t, "", 100000, nil)
	defer clusterRPC.Close()

	snapshotDir := t.TempDir()
	cfg := &config.Config{
		Validator: config.Validator{
			RPCURL:              "http://127.0.0.1:1", // nothing listening
			ActiveIdentityPubkey: "ActivePubkey",
		},
		Cluster: config.Cluster{Name: "testnet", RPCURL: clusterRPC.URL},
		Snapshots: config.Snapshots{
			Directory: snapshotDir,
			Discovery: config.Discovery{
				Candidates: config.DiscoveryCandidates{MinSuitableFull: 3, MinSuitableIncremental: 5, SortOrder: "latency"},
				Probe:      config.DiscoveryProbe{MaxLatency: "5s", MaxLatencyDuration: 5 * time.Second, Concurrency: 10},
			},
			Download: config.SnapshotsDownload{
				MinSpeedCheckDelay: "0s",
				Connections:        1,
			},
			Age: config.SnapshotsAge{
				Remote: config.SnapshotsRemoteAge{MaxSlots: 1300},
				Local:  config.SnapshotsLocalAge{MaxIncrementalSlots: 1300},
			},
		},
	}

	k := New(cfg)
	// Will proceed past identity check (unknown role) but fail at cluster nodes
	// since we returned empty nodes — that's fine, we're testing it doesn't bail on RPC error
	err := k.Run(context.Background())
	// Should get "no suitable snapshot nodes found" since cluster returns empty
	if err == nil {
		t.Error("expected error due to no nodes, but got nil")
	}
}

func TestAssessFreshness(t *testing.T) {
	tests := []struct {
		name          string
		files         []string
		currentSlot   uint64
		maxIncAge     int
		maxFullAge    int
		expectedMode  downloadMode
	}{
		{
			name:         "no snapshots",
			currentSlot:  100000,
			maxIncAge:    1300,
			maxFullAge:   5000,
			expectedMode: modeFull,
		},
		{
			name:         "fresh snapshot",
			files:        []string{"snapshot-99500-Hash.tar.zst"},
			currentSlot:  100000,
			maxIncAge:    1300,
			maxFullAge:   5000,
			expectedMode: modeSkip,
		},
		{
			name:         "slightly old — incremental",
			files:        []string{"snapshot-97000-Hash.tar.zst"},
			currentSlot:  100000,
			maxIncAge:    1300,
			maxFullAge:   5000,
			expectedMode: modeIncremental,
		},
		{
			name:         "very old — still tries incremental first",
			files:        []string{"snapshot-90000-Hash.tar.zst"},
			currentSlot:  100000,
			maxIncAge:    1300,
			maxFullAge:   5000,
			expectedMode: modeIncremental,
		},
		{
			name:         "no full snapshot — needs full",
			files:        []string{"incremental-snapshot-90000-95000-Inc.tar.zst"},
			currentSlot:  100000,
			maxIncAge:    1300,
			maxFullAge:   5000,
			expectedMode: modeFull,
		},
		{
			name:         "fresh incremental extends range",
			files:        []string{"snapshot-95000-Hash.tar.zst", "incremental-snapshot-95000-99500-Inc.tar.zst"},
			currentSlot:  100000,
			maxIncAge:    1300,
			maxFullAge:   5000,
			expectedMode: modeSkip,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, f := range tt.files {
				os.WriteFile(filepath.Join(dir, f), []byte("data"), 0644)
			}

			cfg := &config.Config{
				Snapshots: config.Snapshots{
					Directory: dir,
					Age: config.SnapshotsAge{
						Remote: config.SnapshotsRemoteAge{MaxSlots: tt.maxFullAge},
						Local: config.SnapshotsLocalAge{
							MaxIncrementalSlots: tt.maxIncAge,
						},
					},
				},
			}
			k := &Keeper{cfg: cfg}

			mode, _, err := k.assessFreshness(tt.currentSlot)
			if err != nil {
				t.Fatal(err)
			}
			if mode != tt.expectedMode {
				t.Errorf("expected mode %q, got %q", tt.expectedMode, mode)
			}
		})
	}
}

func TestRun_FullDownload_EndToEnd(t *testing.T) {
	snapshotData := []byte("fake snapshot data for testing purposes")
	snapshotFilename := "snapshot-100000-HashA.tar.zst"

	snapServer := snapshotServer(t, snapshotFilename, snapshotData)
	defer snapServer.Close()

	snapAddr := snapServer.URL
	localRPC := rpcServer(t, "PassivePubkey", 100100, nil)
	defer localRPC.Close()

	clusterRPC := rpcServer(t, "", 100100, []map[string]any{
		{"pubkey": "node1", "gossip": "10.0.0.1:8001", "rpc": snapAddr},
	})
	defer clusterRPC.Close()

	snapshotDir := t.TempDir()
	cfg := &config.Config{
		Validator: config.Validator{
			RPCURL:              localRPC.URL,
			ActiveIdentityPubkey: "ActivePubkey",
		},
		Cluster: config.Cluster{Name: "testnet", RPCURL: clusterRPC.URL},
		Snapshots: config.Snapshots{
			Directory: snapshotDir,
			Discovery: config.Discovery{
				Candidates: config.DiscoveryCandidates{MinSuitableFull: 3, MinSuitableIncremental: 5, SortOrder: "latency"},
				Probe:      config.DiscoveryProbe{MaxLatency: "5s", MaxLatencyDuration: 5 * time.Second, Concurrency: 10},
			},
			Download: config.SnapshotsDownload{
				MinSpeedCheckDelay: "0s",
				Connections:        1,
				Timeout:            "1m",
			},
			Age: config.SnapshotsAge{
				Remote: config.SnapshotsRemoteAge{MaxSlots: 1300},
				Local:  config.SnapshotsLocalAge{MaxIncrementalSlots: 1300},
			},
		},
	}

	k := New(cfg)
	err := k.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// Verify the snapshot was downloaded
	downloadedPath := filepath.Join(snapshotDir, snapshotFilename)
	data, err := os.ReadFile(downloadedPath)
	if err != nil {
		t.Fatalf("snapshot file not found: %v", err)
	}
	if string(data) != string(snapshotData) {
		t.Errorf("snapshot content mismatch")
	}

	// suppress unused
	_ = fmt.Sprintf
}

// pairedSnapshotServer serves both full and incremental snapshot HEAD redirects and GET data.
func pairedSnapshotServer(t *testing.T, fullFilename, incrFilename string, fullData, incrData []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			switch {
			case strings.HasSuffix(r.URL.Path, "snapshot.tar.bz2") && !strings.Contains(r.URL.Path, "incremental"):
				w.Header().Set("Location", "/"+fullFilename)
				w.WriteHeader(http.StatusFound)
			case strings.Contains(r.URL.Path, "incremental-snapshot.tar.bz2"):
				w.Header().Set("Location", "/"+incrFilename)
				w.WriteHeader(http.StatusFound)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
			return
		}

		// GET — serve snapshot data based on filename
		switch {
		case strings.Contains(r.URL.Path, "incremental"):
			w.Header().Set("Content-Length", strconv.Itoa(len(incrData)))
			w.WriteHeader(http.StatusOK)
			w.Write(incrData)
		default:
			w.Header().Set("Content-Length", strconv.Itoa(len(fullData)))
			w.WriteHeader(http.StatusOK)
			w.Write(fullData)
		}
	}))
}

func TestRun_PairedDownload_EndToEnd(t *testing.T) {
	fullData := []byte("fake full snapshot data")
	incrData := []byte("fake incremental snapshot data")
	fullFilename := "snapshot-100000-HashFull.tar.zst"
	incrFilename := "incremental-snapshot-100000-100500-HashInc.tar.zst"

	snapServer := pairedSnapshotServer(t, fullFilename, incrFilename, fullData, incrData)
	defer snapServer.Close()

	snapAddr := snapServer.URL
	localRPC := rpcServer(t, "PassivePubkey", 100600, nil)
	defer localRPC.Close()

	clusterRPC := rpcServer(t, "", 100600, []map[string]any{
		{"pubkey": "node1", "gossip": "10.0.0.1:8001", "rpc": snapAddr},
	})
	defer clusterRPC.Close()

	snapshotDir := t.TempDir()
	cfg := &config.Config{
		Validator: config.Validator{
			RPCURL:               localRPC.URL,
			ActiveIdentityPubkey: "ActivePubkey",
		},
		Cluster: config.Cluster{Name: "testnet", RPCURL: clusterRPC.URL},
		Snapshots: config.Snapshots{
			Directory: snapshotDir,
			Discovery: config.Discovery{
				Candidates: config.DiscoveryCandidates{MinSuitableFull: 3, MinSuitableIncremental: 5, SortOrder: "latency"},
				Probe:      config.DiscoveryProbe{MaxLatency: "5s", MaxLatencyDuration: 5 * time.Second, Concurrency: 10},
			},
			Download: config.SnapshotsDownload{
				MinSpeedCheckDelay: "0s",
				Connections:        1,
				Timeout:            "1m",
			},
			Age: config.SnapshotsAge{
				Remote: config.SnapshotsRemoteAge{MaxSlots: 1300},
				Local:  config.SnapshotsLocalAge{MaxIncrementalSlots: 1300},
			},
		},
	}

	k := New(cfg)
	err := k.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// Verify both snapshots were downloaded
	fullPath := filepath.Join(snapshotDir, fullFilename)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		t.Fatalf("full snapshot file not found: %v", err)
	}
	if string(data) != string(fullData) {
		t.Errorf("full snapshot content mismatch")
	}

	incrPath := filepath.Join(snapshotDir, incrFilename)
	data, err = os.ReadFile(incrPath)
	if err != nil {
		t.Fatalf("incremental snapshot file not found: %v", err)
	}
	if string(data) != string(incrData) {
		t.Errorf("incremental snapshot content mismatch")
	}
}
