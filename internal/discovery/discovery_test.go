package discovery

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sol-strategies/solana-validator-snapshot-keeper/internal/rpc"
)

func strPtr(s string) *string { return &s }

func TestParseSnapshotFilename_Full(t *testing.T) {
	tests := []struct {
		filename string
		slot     uint64
		wantErr  bool
	}{
		{"snapshot-135501350-AbCdEfGh.tar.zst", 135501350, false},
		{"snapshot-100-XyZw1234.tar.bz2", 100, false},
		{"snapshot-999999999-Hash1234.tar.gz", 999999999, false},
		{"invalid-filename.tar.zst", 0, true},
		{"snapshot-135501350-AbCdEfGh.tar", 0, true}, // uncompressed
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			node, err := parseSnapshotFilename(tt.filename, SnapshotTypeFull)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if node.Slot != tt.slot {
				t.Errorf("expected slot %d, got %d", tt.slot, node.Slot)
			}
			if node.SnapshotType != SnapshotTypeFull {
				t.Errorf("expected type full, got %s", node.SnapshotType)
			}
		})
	}
}

func TestParseSnapshotFilename_Incremental(t *testing.T) {
	tests := []struct {
		filename string
		baseSlot uint64
		slot     uint64
		wantErr  bool
	}{
		{"incremental-snapshot-135501350-135502000-XyZw.tar.zst", 135501350, 135502000, false},
		{"incremental-snapshot-100-200-Hash.tar.bz2", 100, 200, false},
		{"snapshot-100-Hash.tar.zst", 0, 0, true}, // full, not incremental
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			node, err := parseSnapshotFilename(tt.filename, SnapshotTypeIncremental)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if node.BaseSlot != tt.baseSlot {
				t.Errorf("expected base_slot %d, got %d", tt.baseSlot, node.BaseSlot)
			}
			if node.Slot != tt.slot {
				t.Errorf("expected slot %d, got %d", tt.slot, node.Slot)
			}
		})
	}
}

func TestProbeNode_FullSnapshot(t *testing.T) {
	snapshotFilename := "snapshot-135501350-AbCdEfGh.tar.zst"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Errorf("expected HEAD, got %s", r.Method)
		}
		if r.URL.Path == "/snapshot.tar.bz2" {
			w.Header().Set("Location", "/"+snapshotFilename)
			w.WriteHeader(http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	opts := Options{
		MaxLatency:          5 * time.Second, // generous for test
		MaxSnapshotAgeSlots: 1300,
		ProbeConcurrency:    10,
	}

	node, err := probeNode(context.Background(), server.URL, "/snapshot.tar.bz2", 135501400, SnapshotTypeFull, opts)
	if err != nil {
		t.Fatal(err)
	}
	if node.Slot != 135501350 {
		t.Errorf("expected slot 135501350, got %d", node.Slot)
	}
	if node.SlotAge != 50 {
		t.Errorf("expected slot age 50, got %d", node.SlotAge)
	}
	if node.SnapshotType != SnapshotTypeFull {
		t.Errorf("expected full type, got %s", node.SnapshotType)
	}
}

func TestProbeNode_TooOld(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/snapshot-100000-AbCd.tar.zst")
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()

	opts := Options{
		MaxLatency:          5 * time.Second,
		MaxSnapshotAgeSlots: 1300,
		ProbeConcurrency:    10,
	}

	_, err := probeNode(context.Background(), server.URL, "/snapshot.tar.bz2", 200000, SnapshotTypeFull, opts)
	if err == nil {
		t.Error("expected error for too-old snapshot")
	}
}

func TestDiscoverNodes_ConcurrentProbing(t *testing.T) {
	// Create multiple servers simulating different nodes
	servers := make([]*httptest.Server, 5)
	for i := range servers {
		slot := 135501000 + i*100
		filename := fmt.Sprintf("snapshot-%d-Hash%d.tar.zst", slot, i)
		servers[i] = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", "/"+filename)
			w.WriteHeader(http.StatusFound)
		}))
		defer servers[i].Close()
	}

	// Build cluster nodes pointing to our test servers
	var clusterNodes []rpc.ClusterNode
	for _, s := range servers {
		addr := s.URL
		clusterNodes = append(clusterNodes, rpc.ClusterNode{
			Pubkey: "test",
			RPC:    &addr,
		})
	}

	opts := Options{
		MaxLatency:          5 * time.Second,
		MaxSnapshotAgeSlots: 2000,
		ProbeConcurrency:    10,
		SortOrder:           "latency",
	}

	results := DiscoverNodes(context.Background(), clusterNodes, 135501500, SnapshotTypeFull, opts)
	if len(results) != 5 {
		t.Errorf("expected 5 results, got %d", len(results))
	}
}

func TestDiscoverNodes_SortBySlotAge(t *testing.T) {
	slots := []int{135500000, 135501000, 135500500}
	servers := make([]*httptest.Server, len(slots))
	for i, slot := range slots {
		filename := fmt.Sprintf("snapshot-%d-Hash%d.tar.zst", slot, i)
		servers[i] = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", "/"+filename)
			w.WriteHeader(http.StatusFound)
		}))
		defer servers[i].Close()
	}

	var clusterNodes []rpc.ClusterNode
	for _, s := range servers {
		addr := s.URL
		clusterNodes = append(clusterNodes, rpc.ClusterNode{
			Pubkey: "test",
			RPC:    &addr,
		})
	}

	opts := Options{
		MaxLatency:          5 * time.Second,
		MaxSnapshotAgeSlots: 5000,
		ProbeConcurrency:    10,
		SortOrder:           "slot_age",
	}

	results := DiscoverNodes(context.Background(), clusterNodes, 135501500, SnapshotTypeFull, opts)
	if len(results) < 2 {
		t.Fatal("expected at least 2 results")
	}
	// Should be sorted by slot age (ascending), meaning newest slot first
	for i := 1; i < len(results); i++ {
		if results[i].SlotAge < results[i-1].SlotAge {
			t.Errorf("results not sorted by slot_age: %d < %d at index %d", results[i].SlotAge, results[i-1].SlotAge, i)
		}
	}
}

func TestDiscoverIncrementalForBase(t *testing.T) {
	// Server 1: incremental based on slot 135501000
	s1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/incremental-snapshot-135501000-135501500-Hash1.tar.zst")
		w.WriteHeader(http.StatusFound)
	}))
	defer s1.Close()

	// Server 2: incremental based on different slot
	s2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/incremental-snapshot-135500000-135501200-Hash2.tar.zst")
		w.WriteHeader(http.StatusFound)
	}))
	defer s2.Close()

	addr1 := s1.URL
	addr2 := s2.URL
	clusterNodes := []rpc.ClusterNode{
		{Pubkey: "n1", RPC: &addr1},
		{Pubkey: "n2", RPC: &addr2},
	}

	opts := Options{
		MaxLatency:          5 * time.Second,
		MaxSnapshotAgeSlots: 5000,
		ProbeConcurrency:    10,
		SortOrder:           "latency",
	}

	results := DiscoverIncrementalForBase(context.Background(), clusterNodes, 135501600, 135501000, opts)
	if len(results) != 1 {
		t.Fatalf("expected 1 matching incremental, got %d", len(results))
	}
	if results[0].BaseSlot != 135501000 {
		t.Errorf("expected base_slot 135501000, got %d", results[0].BaseSlot)
	}
}

func TestDiscoverPairedNodes_HappyPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch r.URL.Path {
		case "/snapshot.tar.bz2":
			w.Header().Set("Location", "/snapshot-100000-HashFull.tar.zst")
			w.WriteHeader(http.StatusFound)
		case "/incremental-snapshot.tar.bz2":
			w.Header().Set("Location", "/incremental-snapshot-100000-100500-HashInc.tar.zst")
			w.WriteHeader(http.StatusFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	addr := server.URL
	nodes := []rpc.ClusterNode{{Pubkey: "n1", RPC: &addr}}

	opts := Options{
		MaxLatency:          5 * time.Second,
		MaxSnapshotAgeSlots: 1300,
		ProbeConcurrency:    10,
		SortOrder:           "latency",
	}

	results := DiscoverPairedNodes(context.Background(), nodes, 100600, opts)
	if len(results) != 1 {
		t.Fatalf("expected 1 paired result, got %d", len(results))
	}
	if results[0].Full.Slot != 100000 {
		t.Errorf("expected full slot 100000, got %d", results[0].Full.Slot)
	}
	if results[0].Incremental.Slot != 100500 {
		t.Errorf("expected incremental slot 100500, got %d", results[0].Incremental.Slot)
	}
	if results[0].Incremental.BaseSlot != 100000 {
		t.Errorf("expected incremental base_slot 100000, got %d", results[0].Incremental.BaseSlot)
	}
}

func TestDiscoverPairedNodes_IncrementalTooOld(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/snapshot.tar.bz2":
			w.Header().Set("Location", "/snapshot-100000-HashFull.tar.zst")
			w.WriteHeader(http.StatusFound)
		case "/incremental-snapshot.tar.bz2":
			// Incremental is old: slot 100200, current 102000, age 1800 > max 1300
			w.Header().Set("Location", "/incremental-snapshot-100000-100200-HashInc.tar.zst")
			w.WriteHeader(http.StatusFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	addr := server.URL
	nodes := []rpc.ClusterNode{{Pubkey: "n1", RPC: &addr}}

	opts := Options{
		MaxLatency:          5 * time.Second,
		MaxSnapshotAgeSlots: 1300,
		ProbeConcurrency:    10,
	}

	results := DiscoverPairedNodes(context.Background(), nodes, 102000, opts)
	if len(results) != 0 {
		t.Errorf("expected 0 results (incremental too old), got %d", len(results))
	}
}

func TestDiscoverPairedNodes_NoIncremental(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/snapshot.tar.bz2":
			w.Header().Set("Location", "/snapshot-100000-HashFull.tar.zst")
			w.WriteHeader(http.StatusFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	addr := server.URL
	nodes := []rpc.ClusterNode{{Pubkey: "n1", RPC: &addr}}

	opts := Options{
		MaxLatency:          5 * time.Second,
		MaxSnapshotAgeSlots: 1300,
		ProbeConcurrency:    10,
	}

	results := DiscoverPairedNodes(context.Background(), nodes, 100600, opts)
	if len(results) != 0 {
		t.Errorf("expected 0 results (no incremental), got %d", len(results))
	}
}

func TestDiscoverPairedNodes_BaseSlotMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/snapshot.tar.bz2":
			w.Header().Set("Location", "/snapshot-100000-HashFull.tar.zst")
			w.WriteHeader(http.StatusFound)
		case "/incremental-snapshot.tar.bz2":
			// Base slot 99000 doesn't match full slot 100000
			w.Header().Set("Location", "/incremental-snapshot-99000-100500-HashInc.tar.zst")
			w.WriteHeader(http.StatusFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	addr := server.URL
	nodes := []rpc.ClusterNode{{Pubkey: "n1", RPC: &addr}}

	opts := Options{
		MaxLatency:          5 * time.Second,
		MaxSnapshotAgeSlots: 1300,
		ProbeConcurrency:    10,
	}

	results := DiscoverPairedNodes(context.Background(), nodes, 100600, opts)
	if len(results) != 0 {
		t.Errorf("expected 0 results (base slot mismatch), got %d", len(results))
	}
}

func TestProbeNode_NoAgeFilter(t *testing.T) {
	// When MaxSnapshotAgeSlots is 0, age check should be skipped
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/snapshot-100000-HashFull.tar.zst")
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()

	opts := Options{
		MaxLatency:          5 * time.Second,
		MaxSnapshotAgeSlots: 0, // no age filter
		ProbeConcurrency:    10,
	}

	// Very old snapshot: current 200000, slot 100000, age 100000 — would normally be rejected
	node, err := probeNode(context.Background(), server.URL, "/snapshot.tar.bz2", 200000, SnapshotTypeFull, opts)
	if err != nil {
		t.Fatalf("expected no error with age filter disabled, got: %v", err)
	}
	if node.Slot != 100000 {
		t.Errorf("expected slot 100000, got %d", node.Slot)
	}
	if node.SlotAge != 100000 {
		t.Errorf("expected slot age 100000, got %d", node.SlotAge)
	}
}

func TestExtractRPCAddresses(t *testing.T) {
	addr1 := "10.0.0.1:8899"
	addr2 := "http://10.0.0.2:8899"
	nodes := []rpc.ClusterNode{
		{Pubkey: "a", RPC: &addr1},
		{Pubkey: "b", RPC: &addr2},
		{Pubkey: "c", RPC: nil},
	}

	addrs := extractRPCAddresses(nodes)
	if len(addrs) != 2 {
		t.Fatalf("expected 2 addresses, got %d", len(addrs))
	}
	if addrs[0] != "http://10.0.0.1:8899" {
		t.Errorf("expected http:// prefix added, got %q", addrs[0])
	}
	if addrs[1] != "http://10.0.0.2:8899" {
		t.Errorf("expected unchanged, got %q", addrs[1])
	}
}
