package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/charmbracelet/log"

	"github.com/sol-strategies/solana-validator-snapshot-keeper/internal/config"
	"github.com/sol-strategies/solana-validator-snapshot-keeper/internal/keeper"
)

func logger() *log.Logger { return log.Default().WithPrefix("manager") }

const lockFilename = "solana-validator-snapshot-keeper.lock"

type lockInfo struct {
	PID       int    `json:"pid"`
	StartedAt string `json:"started_at"`
}

type Manager struct {
	config *config.Config
	keeper *keeper.Keeper
}

func New(cfg *config.Config) *Manager {
	return &Manager{
		config: cfg,
		keeper: keeper.New(cfg),
	}
}

func (m *Manager) RunOnce() error {
	logger().Info("running snapshot keeper (once)")

	if err := m.acquireLock(); err != nil {
		return err
	}
	defer m.releaseLock()

	return m.keeper.Run(context.Background())
}

func (m *Manager) RunOnInterval(interval time.Duration) error {
	logger().Info("running snapshot keeper on interval", "interval", interval)

	for {
		next := calculateNextBoundary(time.Now(), interval)
		sleepDuration := time.Until(next)
		logger().Info(fmt.Sprintf("next run in %s at %s", sleepDuration.Round(time.Second), next.UTC().Format("2006-01-02T15:04:05.000Z")))

		time.Sleep(sleepDuration)

		if err := m.acquireLock(); err != nil {
			logger().Warn("skipping cycle, lock held by another process", "error", err)
			continue
		}

		if err := m.keeper.Run(context.Background()); err != nil {
			logger().Error("run failed", "error", err)
		}

		m.releaseLock()
	}
}

func (m *Manager) lockPath() string {
	return filepath.Join(m.config.Snapshots.Directory, lockFilename)
}

func (m *Manager) acquireLock() error {
	lockPath := m.lockPath()

	data, err := os.ReadFile(lockPath)
	if err == nil {
		// Lock file exists — check if the process is still alive
		var info lockInfo
		if err := json.Unmarshal(data, &info); err == nil {
			if isProcessAlive(info.PID) {
				return fmt.Errorf("another instance is running (PID: %d, started: %s)", info.PID, info.StartedAt)
			}
			logger().Warn("stale lock file found, overwriting", "stale_pid", info.PID)
		}
	}

	info := lockInfo{
		PID:       os.Getpid(),
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}
	lockData, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling lock info: %w", err)
	}

	if err := os.WriteFile(lockPath, lockData, 0644); err != nil {
		return fmt.Errorf("writing lock file: %w", err)
	}

	logger().Debug("lock acquired", "path", lockPath, "pid", info.PID)
	return nil
}

func (m *Manager) releaseLock() {
	lockPath := m.lockPath()
	if err := os.Remove(lockPath); err != nil && !os.IsNotExist(err) {
		logger().Error("failed to remove lock file", "path", lockPath, "error", err)
	} else {
		logger().Debug("lock released", "path", lockPath)
	}
}

func isProcessAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 checks if the process exists without actually sending a signal
	err = process.Signal(syscall.Signal(0))
	return err == nil
}

func calculateNextBoundary(now time.Time, interval time.Duration) time.Time {
	if interval <= 0 {
		return now
	}
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	elapsed := now.Sub(midnight)
	intervals := int64(elapsed / interval)
	next := midnight.Add(time.Duration(intervals+1) * interval)
	if next.Before(now) || next.Equal(now) {
		next = next.Add(interval)
	}
	return next
}
