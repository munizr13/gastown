package deacon

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPatrolGuardRejectsUncertainAndActiveHolds(t *testing.T) {
	for _, body := range []string{`{"paused":true,"reason":"incident"}`, `{`, `{}`, `null`, `{"paused":null}`, `{"paused":"false"}`} {
		t.Run(body, func(t *testing.T) {
			town := t.TempDir()
			if err := os.MkdirAll(filepath.Dir(GetPauseFile(town)), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(GetPauseFile(town), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			if err := CheckPatrolAllowed(town); !errors.Is(err, ErrPatrolHeld) {
				t.Fatalf("guard = %v", err)
			}
			// No tmux or poller is configured. Touching either would panic: even
			// a zombie or an already-running session must be left alone under a hold.
			m := &Manager{townRoot: town}
			if err := m.Start(""); !errors.Is(err, ErrPatrolHeld) {
				t.Fatalf("Start = %v", err)
			}
			if _, err := os.Stat(filepath.Join(town, "deacon")); !os.IsNotExist(err) {
				t.Fatal("Start created runtime state")
			}
		})
	}
}

func TestPatrolGuardPauseResumeAndEstop(t *testing.T) {
	town := t.TempDir()
	if err := CheckPatrolAllowed(town); err != nil {
		t.Fatal(err)
	}
	if err := Pause(town, "incident", "human"); err != nil {
		t.Fatal(err)
	}
	if err := CheckPatrolAllowed(town); !errors.Is(err, ErrPatrolHeld) {
		t.Fatal(err)
	}
	if err := Resume(town); err != nil {
		t.Fatal(err)
	}
	if err := CheckPatrolAllowed(town); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(town, "ESTOP"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := CheckPatrolAllowed(town); !errors.Is(err, ErrPatrolHeld) {
		t.Fatal(err)
	}
}

func TestPatrolGuardReadError(t *testing.T) {
	town := t.TempDir()
	if err := os.MkdirAll(GetPauseFile(town), 0755); err != nil {
		t.Fatal(err)
	}
	if err := CheckPatrolAllowed(town); !errors.Is(err, ErrPatrolHeld) {
		t.Fatal(err)
	}
}

func TestPatrolGuardRejectsMissingTownAndDanglingPause(t *testing.T) {
	town := t.TempDir()
	for _, root := range []string{"", filepath.Join(town, "missing-town")} {
		if err := CheckPatrolAllowed(root); !errors.Is(err, ErrPatrolHeld) {
			t.Fatalf("root %q: %v", root, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(GetPauseFile(town)), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(town, "missing-state"), GetPauseFile(town)); err != nil {
		t.Fatal(err)
	}
	if err := CheckPatrolAllowed(town); !errors.Is(err, ErrPatrolHeld) {
		t.Fatalf("dangling pause file allowed patrol: %v", err)
	}
}
