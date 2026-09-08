package polecat

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/deacon"
	"github.com/steveyegge/gastown/internal/rig"
)

func TestPolecatStartHonorsHoldBeforeTouchingSession(t *testing.T) {
	town := t.TempDir()
	if err := deacon.Pause(town, "incident", "human"); err != nil {
		t.Fatal(err)
	}
	m := &SessionManager{rig: &rig.Rig{Name: "test_rig", Path: filepath.Join(town, "test_rig"), Polecats: []string{"garnet"}}}
	if err := m.Start("garnet", SessionStartOptions{}); !errors.Is(err, deacon.ErrPatrolHeld) {
		t.Fatalf("Start = %v", err)
	}
}
