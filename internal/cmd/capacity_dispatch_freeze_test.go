package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/deacon"
	"github.com/steveyegge/gastown/internal/estop"
)

// --- Dispatch freeze gate (renascentia, 2026-08-29) ---
// Human stop controls must actually stop dispatch. Before this gate,
// deacon.IsPaused had zero dispatch-path readers and ESTOP was checked only
// in the daemon heartbeat (27 post-ESTOP spawns, 2026-08-03).

func TestDispatchFreezeReasonClean(t *testing.T) {
	town := t.TempDir()
	if reason := dispatchFreezeReason(town); reason != "" {
		t.Fatalf("clean town should not be frozen, got: %s", reason)
	}
}

func TestDispatchFreezeReasonEstop(t *testing.T) {
	town := t.TempDir()
	if err := estop.Activate(town, "test", "drill"); err != nil {
		t.Fatalf("activate estop: %v", err)
	}
	reason := dispatchFreezeReason(town)
	if !strings.Contains(reason, "E-STOP") {
		t.Fatalf("expected E-STOP freeze, got: %q", reason)
	}
}

func TestDispatchFreezeReasonDeaconPause(t *testing.T) {
	town := t.TempDir()
	if err := deacon.Pause(town, "containment drill", "human"); err != nil {
		t.Fatalf("pause deacon: %v", err)
	}
	reason := dispatchFreezeReason(town)
	if !strings.Contains(reason, "Deacon is paused by human") {
		t.Fatalf("expected deacon-pause freeze, got: %q", reason)
	}
	if !strings.Contains(reason, "containment drill") {
		t.Fatalf("freeze reason should carry the pause reason, got: %q", reason)
	}
}

func TestDispatchFreezeReasonCorruptPauseFailsClosed(t *testing.T) {
	town := t.TempDir()
	pauseFile := deacon.GetPauseFile(town)
	if err := os.MkdirAll(filepath.Dir(pauseFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pauseFile, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Unreadable pause state cannot authorize dispatch.
	if reason := dispatchFreezeReason(town); !strings.Contains(reason, "cannot read Deacon pause state") {
		t.Fatalf("corrupt pause file should block dispatch, got: %s", reason)
	}
}

// TestDispatchScheduledWorkHonorsFreeze proves the gate short-circuits the
// real dispatch entrypoint: on a paused or e-stopped town, dispatchScheduledWork
// returns (0, nil) without needing any town scaffold — meaning it returned
// BEFORE planning, locking, or spawning anything.
func TestDispatchScheduledWorkHonorsFreeze(t *testing.T) {
	pausedTown := t.TempDir()
	if err := deacon.Pause(pausedTown, "drill", "human"); err != nil {
		t.Fatal(err)
	}
	n, err := dispatchScheduledWork(pausedTown, "test", 0, false)
	if err != nil || n != 0 {
		t.Fatalf("paused town: want (0, nil), got (%d, %v)", n, err)
	}

	estopTown := t.TempDir()
	if err := estop.Activate(estopTown, "test", "drill"); err != nil {
		t.Fatal(err)
	}
	n, err = dispatchScheduledWork(estopTown, "test", 0, false)
	if err != nil || n != 0 {
		t.Fatalf("e-stopped town: want (0, nil), got (%d, %v)", n, err)
	}

	// Dry-run must ALSO report frozen rather than planning.
	n, err = dispatchScheduledWork(pausedTown, "test", 0, true)
	if err != nil || n != 0 {
		t.Fatalf("paused dry-run: want (0, nil), got (%d, %v)", n, err)
	}
}
