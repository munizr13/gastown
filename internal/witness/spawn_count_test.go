package witness

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/gastown/internal/config"
)

func TestReserveBeadRespawnLimitsConcurrentProcesses(t *testing.T) {
	town := t.TempDir()
	const attempts = 17
	var wg sync.WaitGroup
	results := make(chan int, attempts)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestReserveBeadRespawnChild$")
			cmd.Env = append(os.Environ(), "GT_REPAIR_RESPAWN_TOWN="+town)
			if err := cmd.Run(); err == nil {
				results <- 0
			} else if exit, ok := err.(*exec.ExitError); ok {
				results <- exit.ExitCode()
			} else {
				results <- -1
			}
		}()
	}
	wg.Wait()
	close(results)
	success := 0
	for code := range results {
		if code == 0 {
			success++
		} else if code != 20 {
			t.Fatalf("unexpected child failure: %d", code)
		}
	}
	if success != config.DefaultWitnessMaxBeadRespawns {
		t.Fatalf("admitted %d of 17 concurrent attempts, want %d", success, config.DefaultWitnessMaxBeadRespawns)
	}
	if _, err := ReserveBeadRespawn(town, "bead-race"); err == nil {
		t.Fatal("reopening state must retain exhausted budget")
	}
	if err := ResetBeadRespawnCount(town, "bead-race"); err != nil {
		t.Fatal(err)
	}
	if count, err := ReserveBeadRespawn(town, "bead-race"); err != nil || count != 1 {
		t.Fatalf("explicit reset: %d, %v", count, err)
	}
}

func TestReserveBeadRespawnChild(t *testing.T) {
	town := os.Getenv("GT_REPAIR_RESPAWN_TOWN")
	if town == "" {
		t.Skip("subprocess helper")
	}
	_, err := ReserveBeadRespawn(town, "bead-race")
	if err != nil {
		if strings.Contains(err.Error(), "respawn limit reached") {
			os.Exit(20)
		}
		t.Fatal(err)
	}
}

func TestRespawnStateCorruptionCannotReopenBudget(t *testing.T) {
	for _, content := range []string{`{`, `null`, `{}`, `{"beads":{"x":null}}`, `{"beads":{"x":{"bead_id":"x","count":-1}}}`} {
		t.Run(content, func(t *testing.T) {
			town := t.TempDir()
			if err := os.MkdirAll(filepath.Join(town, "witness"), 0755); err != nil {
				t.Fatal(err)
			}
			path := beadRespawnStateFile(town)
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			if !ShouldBlockRespawn(town, "x") {
				t.Fatal("corrupt state permitted dispatch")
			}
			if _, err := ReserveBeadRespawn(town, "x"); err == nil {
				t.Fatal("corrupt state permitted reservation")
			}
			if count := RecordBeadRespawn(town, "x"); count >= 0 {
				t.Fatal("witness must not recover without durable tracking")
			}
			if err := ResetBeadRespawnCount(town, "x"); err == nil {
				t.Fatal("reset silently erased unknown counters")
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != content {
				t.Fatal("corrupt source was overwritten")
			}
		})
	}
}

func TestDanglingRespawnStateCannotReopenBudget(t *testing.T) {
	town := t.TempDir()
	path := beadRespawnStateFile(town)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(town, "missing-state"), path); err != nil {
		t.Fatal(err)
	}
	if !ShouldBlockRespawn(town, "x") {
		t.Fatal("dangling counter allowed dispatch")
	}
	if _, err := ReserveBeadRespawn(town, "x"); err == nil {
		t.Fatal("dangling counter allowed reservation")
	}
	if err := ResetBeadRespawnCount(town, "x"); err == nil {
		t.Fatal("reset replaced unreadable evidence")
	}
	if _, err := os.Readlink(path); err != nil {
		t.Fatal("counter evidence was overwritten")
	}
}

func TestRecordBeadRespawn_Increments(t *testing.T) {
	tmpDir := t.TempDir()
	// Create the witness subdirectory so the state file path is valid.
	if err := os.MkdirAll(filepath.Join(tmpDir, "witness"), 0755); err != nil {
		t.Fatal(err)
	}

	count := RecordBeadRespawn(tmpDir, "bead-1")
	if count != 1 {
		t.Errorf("first RecordBeadRespawn = %d, want 1", count)
	}

	count = RecordBeadRespawn(tmpDir, "bead-1")
	if count != 2 {
		t.Errorf("second RecordBeadRespawn = %d, want 2", count)
	}
}

func TestShouldBlockRespawn_Threshold(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, "witness"), 0755); err != nil {
		t.Fatal(err)
	}

	// Below threshold.
	for i := 0; i < config.DefaultWitnessMaxBeadRespawns-1; i++ {
		RecordBeadRespawn(tmpDir, "bead-2")
	}
	if ShouldBlockRespawn(tmpDir, "bead-2") {
		t.Error("ShouldBlockRespawn = true before reaching threshold")
	}

	// At threshold.
	RecordBeadRespawn(tmpDir, "bead-2")
	if !ShouldBlockRespawn(tmpDir, "bead-2") {
		t.Error("ShouldBlockRespawn = false at threshold")
	}
}

func TestResetBeadRespawnCount(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, "witness"), 0755); err != nil {
		t.Fatal(err)
	}

	RecordBeadRespawn(tmpDir, "bead-3")
	RecordBeadRespawn(tmpDir, "bead-3")

	if err := ResetBeadRespawnCount(tmpDir, "bead-3"); err != nil {
		t.Fatalf("ResetBeadRespawnCount error: %v", err)
	}

	if ShouldBlockRespawn(tmpDir, "bead-3") {
		t.Error("ShouldBlockRespawn = true after reset")
	}

	// Re-increment should start from 1.
	count := RecordBeadRespawn(tmpDir, "bead-3")
	if count != 1 {
		t.Errorf("RecordBeadRespawn after reset = %d, want 1", count)
	}
}

func TestRecordBeadRespawn_ConcurrentSafe(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, "witness"), 0755); err != nil {
		t.Fatal(err)
	}

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			RecordBeadRespawn(tmpDir, "bead-race")
		}()
	}
	wg.Wait()

	// After all goroutines, the count must equal the number of increments.
	state, err := loadBeadRespawnState(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := state.Beads["bead-race"]
	if !ok {
		t.Fatal("bead-race record not found")
	}
	if rec.Count != goroutines {
		t.Errorf("concurrent count = %d, want %d", rec.Count, goroutines)
	}
}

func TestShouldBlockRespawn_UnknownBead(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, "witness"), 0755); err != nil {
		t.Fatal(err)
	}

	if ShouldBlockRespawn(tmpDir, "nonexistent") {
		t.Error("ShouldBlockRespawn = true for unknown bead")
	}
}
