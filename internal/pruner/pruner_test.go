package pruner

import (
	"os"
	"path/filepath"
	"testing"
)

func createFile(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}
}

func fileExists(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

func TestPrune_KeepsNewestFull(t *testing.T) {
	dir := t.TempDir()
	createFile(t, dir, "snapshot-100-HashA.tar.zst")
	createFile(t, dir, "snapshot-200-HashB.tar.zst")
	createFile(t, dir, "snapshot-300-HashC.tar.zst")

	if err := Prune(dir); err != nil {
		t.Fatal(err)
	}

	if !fileExists(dir, "snapshot-300-HashC.tar.zst") {
		t.Error("newest full should be kept")
	}
	if fileExists(dir, "snapshot-100-HashA.tar.zst") {
		t.Error("oldest full should be removed")
	}
	if fileExists(dir, "snapshot-200-HashB.tar.zst") {
		t.Error("middle full should be removed")
	}
}

func TestPrune_RemovesOrphanedIncrementals(t *testing.T) {
	dir := t.TempDir()
	createFile(t, dir, "snapshot-300-HashC.tar.zst")
	createFile(t, dir, "incremental-snapshot-300-350-HashD.tar.zst") // matches
	createFile(t, dir, "incremental-snapshot-100-150-HashE.tar.zst") // orphaned

	if err := Prune(dir); err != nil {
		t.Fatal(err)
	}

	if !fileExists(dir, "snapshot-300-HashC.tar.zst") {
		t.Error("full should be kept")
	}
	if !fileExists(dir, "incremental-snapshot-300-350-HashD.tar.zst") {
		t.Error("matching incremental should be kept")
	}
	if fileExists(dir, "incremental-snapshot-100-150-HashE.tar.zst") {
		t.Error("orphaned incremental should be removed")
	}
}

func TestPrune_RemovesTempFiles(t *testing.T) {
	dir := t.TempDir()
	createFile(t, dir, "snapshot-300-HashC.tar.zst")
	createFile(t, dir, "snapshot-200-HashB.tar.zst.tmp")
	createFile(t, dir, "something.partial")

	if err := Prune(dir); err != nil {
		t.Fatal(err)
	}

	if fileExists(dir, "snapshot-200-HashB.tar.zst.tmp") {
		t.Error("temp file should be removed")
	}
	if fileExists(dir, "something.partial") {
		t.Error("partial file should be removed")
	}
}

func TestGetLocalSnapshots(t *testing.T) {
	dir := t.TempDir()
	createFile(t, dir, "snapshot-100-HashA.tar.zst")
	createFile(t, dir, "snapshot-200-HashB.tar.zst")
	createFile(t, dir, "incremental-snapshot-200-250-HashC.tar.zst")
	createFile(t, dir, "unrelated-file.txt")

	snaps, err := GetLocalSnapshots(dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(snaps) != 3 {
		t.Fatalf("expected 3 snapshots, got %d", len(snaps))
	}
}

func TestNewestSlot(t *testing.T) {
	snapshots := []SnapshotFile{
		{Slot: 100, IsFull: true},
		{Slot: 300, IsFull: true},
		{Slot: 250, BaseSlot: 200},
	}

	if got := NewestSlot(snapshots); got != 300 {
		t.Errorf("expected 300, got %d", got)
	}
}

func TestNewestSlot_Empty(t *testing.T) {
	if got := NewestSlot(nil); got != 0 {
		t.Errorf("expected 0 for empty, got %d", got)
	}
}

func TestNewestFullSnapshot(t *testing.T) {
	snapshots := []SnapshotFile{
		{Slot: 100, IsFull: true},
		{Slot: 300, IsFull: true},
		{Slot: 350, BaseSlot: 300}, // incremental, not full
	}

	newest := NewestFullSnapshot(snapshots)
	if newest == nil {
		t.Fatal("expected non-nil")
	}
	if newest.Slot != 300 {
		t.Errorf("expected slot 300, got %d", newest.Slot)
	}
}

func TestNewestFullSnapshot_NoFulls(t *testing.T) {
	snapshots := []SnapshotFile{
		{Slot: 350, BaseSlot: 300}, // incremental only
	}

	if newest := NewestFullSnapshot(snapshots); newest != nil {
		t.Error("expected nil when no full snapshots")
	}
}
