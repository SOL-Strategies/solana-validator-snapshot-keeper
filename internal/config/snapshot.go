package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Discovery struct {
	Candidates DiscoveryCandidates `koanf:"candidates"`
	Probe      DiscoveryProbe      `koanf:"probe"`
}

type DiscoveryCandidates struct {
	MinSuitableFull        int    `koanf:"min_suitable_full"`
	MinSuitableIncremental int    `koanf:"min_suitable_incremental"`
	SortOrder              string `koanf:"sort_order"`
}

type DiscoveryProbe struct {
	Concurrency int    `koanf:"concurrency"`
	MaxLatency  string `koanf:"max_latency"`
	// Parsed
	MaxLatencyDuration time.Duration `koanf:"-"`
}

type Snapshots struct {
	Directory string            `koanf:"directory"`
	Discovery Discovery         `koanf:"discovery"`
	Download  SnapshotsDownload `koanf:"download"`
	Age       SnapshotsAge      `koanf:"age"`
}

type SnapshotsDownload struct {
	MinSpeed           string `koanf:"min_speed"`
	MinSpeedCheckDelay string `koanf:"min_speed_check_delay"`
	Timeout            string `koanf:"timeout"`
	Connections        int    `koanf:"connections"`
	// Parsed
	MinSpeedBytes         int64         `koanf:"-"`
	MinSpeedCheckDelayDur time.Duration `koanf:"-"`
	TimeoutDur            time.Duration `koanf:"-"`
}

type SnapshotsAge struct {
	Remote SnapshotsRemoteAge `koanf:"remote"`
	Local  SnapshotsLocalAge  `koanf:"local"`
}

type SnapshotsRemoteAge struct {
	MaxSlots int `koanf:"max_slots"`
}

type SnapshotsLocalAge struct {
	MaxIncrementalSlots int `koanf:"max_incremental_slots"`
}

func (d *Discovery) Validate() error {
	if d.Candidates.SortOrder != "latency" && d.Candidates.SortOrder != "slot_age" {
		return fmt.Errorf("discovery.candidates.sort_order must be \"latency\" or \"slot_age\", got %q", d.Candidates.SortOrder)
	}
	if d.Probe.MaxLatency != "" {
		dur, err := time.ParseDuration(d.Probe.MaxLatency)
		if err != nil {
			return fmt.Errorf("discovery.probe.max_latency: %w", err)
		}
		if dur <= 0 {
			return fmt.Errorf("discovery.probe.max_latency must be > 0")
		}
		d.Probe.MaxLatencyDuration = dur
	}
	return nil
}

func (s *Snapshots) Validate() error {
	if err := s.Discovery.Validate(); err != nil {
		return err
	}
	if s.Directory == "" {
		return fmt.Errorf("snapshots.directory is required")
	}
	info, err := os.Stat(s.Directory)
	if err != nil {
		return fmt.Errorf("snapshots.directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("snapshots.directory: %s is not a directory", s.Directory)
	}
	probe := filepath.Join(s.Directory, ".snapshot-keeper-probe")
	if err := os.WriteFile(probe, nil, 0644); err != nil {
		return fmt.Errorf("snapshots.directory: not writable: %w", err)
	}
	os.Remove(probe)
	if s.Download.MinSpeed != "" {
		bytes, err := ParseSize(s.Download.MinSpeed)
		if err != nil {
			return fmt.Errorf("snapshots.download.min_speed: %w", err)
		}
		if bytes < 1 {
			return fmt.Errorf("snapshots.download.min_speed must be > 0")
		}
		s.Download.MinSpeedBytes = bytes
	}
	if s.Download.MinSpeedCheckDelay != "" {
		d, err := time.ParseDuration(s.Download.MinSpeedCheckDelay)
		if err != nil {
			return fmt.Errorf("snapshots.download.min_speed_check_delay: %w", err)
		}
		if d < 0 {
			return fmt.Errorf("snapshots.download.min_speed_check_delay must be >= 0")
		}
		s.Download.MinSpeedCheckDelayDur = d
	}
	if s.Download.Timeout != "" {
		d, err := time.ParseDuration(s.Download.Timeout)
		if err != nil {
			return fmt.Errorf("snapshots.download.timeout: %w", err)
		}
		if d <= 0 {
			return fmt.Errorf("snapshots.download.timeout must be > 0")
		}
		s.Download.TimeoutDur = d
	}
	if s.Age.Remote.MaxSlots < 1 {
		return fmt.Errorf("snapshots.age.remote.max_slots must be >= 1")
	}
	if s.Age.Local.MaxIncrementalSlots < 1 {
		return fmt.Errorf("snapshots.age.local.max_incremental_slots must be >= 1")
	}
	if s.Download.Connections < 1 {
		return fmt.Errorf("snapshots.download.connections must be >= 1")
	}
	return nil
}
