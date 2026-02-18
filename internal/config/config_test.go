package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFromFile_WithDefaults(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yml")
	content := `
validator:
  active_identity_pubkey: "TestPubkey123"
`
	if err := os.WriteFile(cfgFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	c := New()
	if err := c.LoadFromFile(cfgFile); err != nil {
		t.Fatal(err)
	}

	// Check defaults are applied
	if c.Log.Level != "info" {
		t.Errorf("expected log.level=info, got %q", c.Log.Level)
	}
	if c.Log.Format != "text" {
		t.Errorf("expected log.format=text, got %q", c.Log.Format)
	}
	if c.Validator.RPCURL != "http://127.0.0.1:8899" {
		t.Errorf("expected default rpc_url, got %q", c.Validator.RPCURL)
	}
	if c.Validator.ActiveIdentityPubkey != "TestPubkey123" {
		t.Errorf("expected active_identity_pubkey=TestPubkey123, got %q", c.Validator.ActiveIdentityPubkey)
	}
	if c.Cluster.Name != "mainnet-beta" {
		t.Errorf("expected cluster.name=mainnet-beta, got %q", c.Cluster.Name)
	}
	if c.Snapshots.Download.MinSpeed != "60mb" {
		t.Errorf("expected snapshot.download.min_speed=60mb, got %q", c.Snapshots.Download.MinSpeed)
	}
	if c.Snapshots.Download.Connections != 8 {
		t.Errorf("expected snapshot.download.connections=8, got %d", c.Snapshots.Download.Connections)
	}
	if c.Snapshots.Age.Local.MaxIncrementalSlots != 1300 {
		t.Errorf("expected snapshot.age.local.max_incremental_slots=1300, got %d", c.Snapshots.Age.Local.MaxIncrementalSlots)
	}
}

func TestLoadFromFile_OverrideDefaults(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yml")
	content := `
log:
  level: debug
  format: json
validator:
  rpc_url: "http://10.0.0.1:8899"
  active_identity_pubkey: "MyPubkey"
cluster:
  name: testnet
  rpc_url: "https://my-rpc.example.com"
snapshots:
  directory: /tmp/snapshots
  discovery:
    candidates:
      sort_order: slot_age
  download:
    connections: 16
`
	if err := os.WriteFile(cfgFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	c := New()
	if err := c.LoadFromFile(cfgFile); err != nil {
		t.Fatal(err)
	}

	if c.Log.Level != "debug" {
		t.Errorf("expected log.level=debug, got %q", c.Log.Level)
	}
	if c.Validator.RPCURL != "http://10.0.0.1:8899" {
		t.Errorf("expected overridden rpc_url, got %q", c.Validator.RPCURL)
	}
	if c.Cluster.Name != "testnet" {
		t.Errorf("expected cluster.name=testnet, got %q", c.Cluster.Name)
	}
	if c.Cluster.RPCURL != "https://my-rpc.example.com" {
		t.Errorf("expected cluster.rpc_url override, got %q", c.Cluster.RPCURL)
	}
	if c.Snapshots.Download.Connections != 16 {
		t.Errorf("expected download.connections=16, got %d", c.Snapshots.Download.Connections)
	}
	if c.Snapshots.Discovery.Candidates.SortOrder != "slot_age" {
		t.Errorf("expected snapshots.discovery.candidates.sort_order=slot_age, got %q", c.Snapshots.Discovery.Candidates.SortOrder)
	}
}

func TestCluster_EffectiveRPCURL(t *testing.T) {
	tests := []struct {
		name     string
		cluster  Cluster
		expected string
	}{
		{"mainnet-beta auto-derive", Cluster{Name: "mainnet-beta"}, "https://api.mainnet-beta.solana.com"},
		{"testnet auto-derive", Cluster{Name: "testnet"}, "https://api.testnet.solana.com"},
		{"override", Cluster{Name: "mainnet-beta", RPCURL: "https://custom.rpc"}, "https://custom.rpc"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cluster.EffectiveRPCURL()
			if got != tt.expected {
				t.Errorf("EffectiveRPCURL() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestValidation_InvalidCluster(t *testing.T) {
	c := &Config{
		Log:       Log{Level: "info", Format: "text"},
		Validator: Validator{RPCURL: "http://localhost:8899", ActiveIdentityPubkey: "test"},
		Cluster:   Cluster{Name: "invalid-cluster"},
		Snapshots: Snapshots{
			Directory: "/tmp",
			Discovery: Discovery{
				Candidates: DiscoveryCandidates{SortOrder: "latency"},
				Probe:      DiscoveryProbe{MaxLatency: "100ms"},
			},
			Download: SnapshotsDownload{
				MinSpeed:    "60mb",
				Connections: 8,
			},
			Age: SnapshotsAge{
				Remote: SnapshotsRemoteAge{MaxSlots: 1300},
				Local:  SnapshotsLocalAge{MaxIncrementalSlots: 1300},
			},
		},
	}
	err := c.Validate()
	if err == nil {
		t.Error("expected validation error for invalid cluster")
	}
}

func TestValidation_MissingDirectory(t *testing.T) {
	s := &Snapshots{
		Directory: "/nonexistent/path/that/should/not/exist",
		Download: SnapshotsDownload{
			MinSpeed:    "60mb",
			Connections: 8,
		},
		Age: SnapshotsAge{
			Remote: SnapshotsRemoteAge{MaxSlots: 1300},
			Local:  SnapshotsLocalAge{MaxIncrementalSlots: 1300},
		},
	}
	err := s.Validate()
	if err == nil {
		t.Error("expected validation error for nonexistent directory")
	}
}

func TestValidation_DirectoryIsFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "not-a-dir")
	os.WriteFile(f, []byte("x"), 0644)

	s := &Snapshots{
		Directory: f,
		Download: SnapshotsDownload{
			MinSpeed:    "60mb",
			Connections: 8,
		},
		Age: SnapshotsAge{
			Remote: SnapshotsRemoteAge{MaxSlots: 1300},
			Local:  SnapshotsLocalAge{MaxIncrementalSlots: 1300},
		},
	}
	err := s.Validate()
	if err == nil {
		t.Error("expected validation error when directory is a file")
	}
}

func TestValidation_InvalidSortOrder(t *testing.T) {
	d := &Discovery{
		Candidates: DiscoveryCandidates{SortOrder: "invalid"},
		Probe:      DiscoveryProbe{MaxLatency: "100ms"},
	}
	err := d.Validate()
	if err == nil {
		t.Error("expected validation error for invalid sort_order")
	}
}
