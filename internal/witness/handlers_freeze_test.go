package witness

import (
	"testing"

	"github.com/steveyegge/gastown/internal/deacon"
	"github.com/steveyegge/gastown/internal/estop"
)

// A freed polecat slot must not re-ignite dispatch during an e-stop or a
// deliberate Deacon pause. Behavioral: on a frozen town the slot-open path
// returns without running the scheduler (Ran=false) and without error —
// no town scaffold exists in the temp dir, so reaching the status read
// would fail; returning clean proves the gate fired first.
func TestSlotOpenSchedulerHonorsFreeze(t *testing.T) {
	pausedTown := t.TempDir()
	if err := deacon.Pause(pausedTown, "drill", "human"); err != nil {
		t.Fatal(err)
	}
	res, err := defaultRunSchedulerForSlotOpen(pausedTown)
	if err != nil {
		t.Fatalf("paused town: unexpected error: %v", err)
	}
	if res.Ran {
		t.Fatal("paused town: scheduler ran from slot-open path")
	}

	estopTown := t.TempDir()
	if err := estop.Activate(estopTown, "test", "drill"); err != nil {
		t.Fatal(err)
	}
	res, err = defaultRunSchedulerForSlotOpen(estopTown)
	if err != nil {
		t.Fatalf("e-stopped town: unexpected error: %v", err)
	}
	if res.Ran {
		t.Fatal("e-stopped town: scheduler ran from slot-open path")
	}
}
