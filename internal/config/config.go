package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/charmbracelet/log"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

type Config struct {
	Log       Log       `koanf:"log"`
	Validator Validator `koanf:"validator"`
	Cluster   Cluster   `koanf:"cluster"`
	Snapshots Snapshots `koanf:"snapshots"`
	Hooks     Hooks     `koanf:"hooks"`
	File      string    `koanf:"-"`
}

func DefaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "config.yml"
	}
	return filepath.Join(home, "solana-validator-snapshot-keeper", "config.yml")
}

func New() *Config {
	return &Config{}
}

func NewFromConfigFile(path string) (*Config, error) {
	c := New()
	if err := c.LoadFromFile(path); err != nil {
		return nil, err
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	c.Log.ConfigureWithLevelString("", false)
	return c, nil
}

func (c *Config) LoadFromFile(path string) error {
	c.File = path

	k := koanf.New(".")

	defaults := map[string]any{
		"log.level":                             "info",
		"log.format":                            "text",
		"log.disable_timestamps":                false,
		"validator.rpc_url":                     "http://127.0.0.1:8899",
		"cluster.name":                          "mainnet-beta",
		"cluster.rpc_url":                       "",
		"snapshots.discovery.candidates.min_suitable_full":        3,
		"snapshots.discovery.candidates.min_suitable_incremental": 5,
		"snapshots.discovery.candidates.sort_order":   "latency",
		"snapshots.discovery.probe.concurrency":       500,
		"snapshots.discovery.probe.max_latency":       "100ms",
		"snapshots.directory":                      "/mnt/accounts/snapshots",
		"snapshots.download.min_speed":             "60mb",
		"snapshots.download.min_speed_check_delay": "7s",
		"snapshots.download.timeout":               "30m",
		"snapshots.download.connections":           8,
		"snapshots.age.remote.max_slots":            1300,
		"snapshots.age.local.max_incremental_slots": 1300,
	}

	for key, val := range defaults {
		if err := k.Set(key, val); err != nil {
			return fmt.Errorf("setting default %s: %w", key, err)
		}
	}

	if err := k.Load(file.Provider(path), yaml.Parser()); err != nil {
		if os.IsNotExist(err) {
			log.Warn("config file not found, using defaults", "path", path)
		} else {
			return fmt.Errorf("loading config file %s: %w", path, err)
		}
	}

	if err := k.Unmarshal("", c); err != nil {
		return fmt.Errorf("unmarshalling config: %w", err)
	}

	return nil
}

func (c *Config) Validate() error {
	if err := c.Log.Validate(); err != nil {
		return fmt.Errorf("log config: %w", err)
	}
	if err := c.Validator.Validate(); err != nil {
		return fmt.Errorf("validator config: %w", err)
	}
	if err := c.Cluster.Validate(); err != nil {
		return fmt.Errorf("cluster config: %w", err)
	}
	if err := c.Snapshots.Validate(); err != nil {
		return fmt.Errorf("snapshots config: %w", err)
	}
	return nil
}
