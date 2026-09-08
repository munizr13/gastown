package dog

import (
	"errors"
	"testing"

	"github.com/steveyegge/gastown/internal/deacon"
)

func TestDogStartHonorsPatrolHoldBeforeTouchingSession(t *testing.T) {
	town := t.TempDir()
	if err := deacon.Pause(town, "incident", "human"); err != nil {
		t.Fatal(err)
	}
	m := &SessionManager{townRoot: town}
	if err := m.Start("alpha", SessionStartOptions{}); !errors.Is(err, deacon.ErrPatrolHeld) {
		t.Fatalf("Start = %v", err)
	}
}
