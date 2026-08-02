package cmd

import (
	"strings"
	"testing"
)

// A nudge whose caller cannot be resolved to a town role is the shape of an
// injected instruction, and every town agent runs with permissions skipped.
// The marker must SAY that, not look like a role name called "unknown".
func TestUnattributedNudgeSenderIsSelfDescribing(t *testing.T) {
	if UnattributedNudgeSender == "unknown" {
		t.Fatal(`sender marker is still the bare "unknown" — it reads like a role name`)
	}
	if strings.TrimSpace(UnattributedNudgeSender) == "" {
		t.Fatal("sender marker must never be blank")
	}
	for _, want := range []string{"UNATTRIBUTED", "read-only"} {
		if !strings.Contains(UnattributedNudgeSender, want) {
			t.Errorf("marker should contain %q so the receiving agent knows what to do; got %q", want, UnattributedNudgeSender)
		}
	}
}

// Doctrine keys on this string, so a rename must be a deliberate, visible act
// rather than a silent drift that quietly disarms the read-only rule.
func TestUnattributedMarkerIsStableForDoctrine(t *testing.T) {
	const documented = "UNATTRIBUTED (sender could not be resolved to a town role — read-only per nudge-provenance doctrine)"
	if UnattributedNudgeSender != documented {
		t.Errorf("marker changed without updating ~/gt/CLAUDE.md nudge-provenance doctrine:\n got:  %q\n want: %q", UnattributedNudgeSender, documented)
	}
}
