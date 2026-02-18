package pruner

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"

	"github.com/charmbracelet/log"
)

func logger() *log.Logger { return log.Default().WithPrefix("pruner") }

var (
	fullSnapshotRe        = regexp.MustCompile(`^snapshot-(\d+)-[A-Za-z0-9]+\.tar\.(zst|bz2|gz)$`)
	incrementalSnapshotRe = regexp.MustCompile(`^incremental-snapshot-(\d+)-(\d+)-[A-Za-z0-9]+\.tar\.(zst|bz2|gz)$`)
	tempFileRe            = regexp.MustCompile(`\.(tmp|partial)$`)
)

// SnapshotFile represents a parsed snapshot file on disk.
type SnapshotFile struct {
	Path     string
	Slot     uint64
	BaseSlot uint64 // only for incrementals
	IsFull   bool
}

// Prune removes old snapshots, keeping only the most recent full snapshot
// and incrementals that match its base slot. It also removes temp files.
func Prune(snapshotDir string) error {
	entries, err := os.ReadDir(snapshotDir)
	if err != nil {
		return err
	}

	var fulls []SnapshotFile
	var incrementals []SnapshotFile
	var tempFiles []string

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()

		if tempFileRe.MatchString(name) {
			tempFiles = append(tempFiles, filepath.Join(snapshotDir, name))
			continue
		}

		if matches := fullSnapshotRe.FindStringSubmatch(name); matches != nil {
			slot, _ := strconv.ParseUint(matches[1], 10, 64)
			fulls = append(fulls, SnapshotFile{
				Path:   filepath.Join(snapshotDir, name),
				Slot:   slot,
				IsFull: true,
			})
			continue
		}

		if matches := incrementalSnapshotRe.FindStringSubmatch(name); matches != nil {
			baseSlot, _ := strconv.ParseUint(matches[1], 10, 64)
			slot, _ := strconv.ParseUint(matches[2], 10, 64)
			incrementals = append(incrementals, SnapshotFile{
				Path:     filepath.Join(snapshotDir, name),
				Slot:     slot,
				BaseSlot: baseSlot,
			})
			continue
		}
	}

	// Remove temp files
	for _, f := range tempFiles {
		logger().Warn("removing temp file", "file", filepath.Base(f))
		os.Remove(f)
	}

	if len(fulls) == 0 {
		return nil
	}

	// Sort fulls by slot descending, keep the newest
	sort.Slice(fulls, func(i, j int) bool {
		return fulls[i].Slot > fulls[j].Slot
	})

	newestFull := fulls[0]

	// Remove older full snapshots
	for _, f := range fulls[1:] {
		logger().Warn(fmt.Sprintf("pruning old full snapshot %s", f.Path))
		os.Remove(f.Path)
	}

	// Among incrementals matching the newest full, keep only the newest one.
	// Remove all others (orphaned or older).
	sort.Slice(incrementals, func(i, j int) bool {
		return incrementals[i].Slot > incrementals[j].Slot
	})

	keptIncremental := false
	for _, inc := range incrementals {
		if inc.BaseSlot != newestFull.Slot {
			logger().Warn(fmt.Sprintf("pruning orphaned incremental snapshot - base slot %d != newest full slot %d", inc.BaseSlot, newestFull.Slot), "file", inc.Path)
			os.Remove(inc.Path)
		} else if keptIncremental {
			logger().Warn("pruning older incremental snapshot", "file", inc.Path)
			os.Remove(inc.Path)
		} else {
			keptIncremental = true
		}
	}

	return nil
}

// GetLocalSnapshots returns parsed snapshot files from the given directory.
func GetLocalSnapshots(snapshotDir string) ([]SnapshotFile, error) {
	entries, err := os.ReadDir(snapshotDir)
	if err != nil {
		return nil, err
	}

	var snapshots []SnapshotFile
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()

		if matches := fullSnapshotRe.FindStringSubmatch(name); matches != nil {
			slot, _ := strconv.ParseUint(matches[1], 10, 64)
			snapshots = append(snapshots, SnapshotFile{
				Path:   filepath.Join(snapshotDir, name),
				Slot:   slot,
				IsFull: true,
			})
		} else if matches := incrementalSnapshotRe.FindStringSubmatch(name); matches != nil {
			baseSlot, _ := strconv.ParseUint(matches[1], 10, 64)
			slot, _ := strconv.ParseUint(matches[2], 10, 64)
			snapshots = append(snapshots, SnapshotFile{
				Path:     filepath.Join(snapshotDir, name),
				Slot:     slot,
				BaseSlot: baseSlot,
			})
		}
	}

	return snapshots, nil
}

// NewestSlot returns the highest slot number across all local snapshots.
// Returns 0 if no snapshots exist.
func NewestSlot(snapshots []SnapshotFile) uint64 {
	var newest uint64
	for _, s := range snapshots {
		if s.Slot > newest {
			newest = s.Slot
		}
	}
	return newest
}

// NewestFullSnapshot returns the full snapshot with the highest slot.
// Returns nil if no full snapshots exist.
func NewestFullSnapshot(snapshots []SnapshotFile) *SnapshotFile {
	var newest *SnapshotFile
	for i, s := range snapshots {
		if s.IsFull && (newest == nil || s.Slot > newest.Slot) {
			newest = &snapshots[i]
		}
	}
	return newest
}
