package logger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
)

// TestRotation_RollsWhenExceedingMaxSize writes past MaxSizeMB into a temp dir
// and asserts lumberjack produced at least one rotated backup alongside the
// active file — proving rotation is actually wired through newLogger, not just
// that logs land on disk. Fully isolated: temp dir, no production path touched.
func TestRotation_RollsWhenExceedingMaxSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "aigis.log")

	lg, err := NewWithRotation("info", RotationConfig{
		Enabled:    true,
		Filename:   path,
		MaxSizeMB:  1, // smallest practical roll threshold
		MaxBackups: 3,
		MaxAgeDays: 1,
		Compress:   false, // keep backups as plain files so the assertion is simple
	})
	if err != nil {
		t.Fatalf("NewWithRotation: %v", err)
	}

	// ~2 KiB per line; ~1500 lines comfortably exceeds the 1 MiB threshold.
	big := strings.Repeat("x", 2048)
	for range 1500 {
		lg.Info("rotation load", zap.String("payload", big))
	}
	_ = lg.Sync()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) < 2 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("expected the active file + at least one rotated backup, got %d: %v", len(entries), names)
	}

	// The active file must still exist after rotation.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("active log file missing after rotation: %v", err)
	}
}

// TestRotation_DisabledDoesNotRoll confirms rotation is opt-in: with Enabled
// false, the same load that rolled above produces exactly one file (logrotate,
// not lumberjack, would own rotation here).
func TestRotation_DisabledDoesNotRoll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "aigis.log")

	rot := DefaultRotation() // Enabled is false by default
	rot.Filename = path
	rot.MaxSizeMB = 1
	lg, err := NewWithRotation("info", rot)
	if err != nil {
		t.Fatalf("NewWithRotation: %v", err)
	}

	big := strings.Repeat("x", 2048)
	for range 1500 {
		lg.Info("no-roll load", zap.String("payload", big))
	}
	_ = lg.Sync()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("rotation disabled should leave a single file, got %d: %v", len(entries), names)
	}
}
