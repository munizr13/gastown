package cmd

import (
	"os"
	"strings"
	"testing"
)

// --- Supersede fail-loud guards (renascentia, 2026-08-29) ---
// The 2026-07-30 patch fixed the MR *lookup* silent failure; the supersede
// *close* still warned-and-continued, leaving a stale OPEN MR with nothing
// escalating — the double-submit failure shape through a different door.
// These pin the fail-loud behavior at the source level (same style as the
// reaper's agent-bead guards; behavioral proof happens in the live drill).

func readDoneSource(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("done.go")
	if err != nil {
		t.Fatalf("read done.go: %v", err)
	}
	return string(data)
}

func TestSupersedeCloseFailureIsRecorded(t *testing.T) {
	source := readDoneSource(t)
	idx := strings.Index(source, "could not supersede old MR")
	if idx == -1 {
		t.Fatal("supersede-close failure branch not found")
	}
	// The failure branch must append to doneErrors (summary) and comment the
	// new MR (refinery-visible record) — not merely PrintWarning.
	window := source[idx-800 : idx+900]
	if !strings.Contains(window, "doneErrors = append(doneErrors") {
		t.Fatal("supersede-close failure no longer recorded in doneErrors — stale open MRs would be silent again")
	}
	if !strings.Contains(window, "SUPERSEDE FAILED") {
		t.Fatal("supersede-close failure no longer commented on the new MR — refinery loses the stale-MR signal")
	}
}

func TestSupersedeLookupFailureIsRecorded(t *testing.T) {
	source := readDoneSource(t)
	idx := strings.Index(source, "could not look up prior MRs to supersede")
	if idx == -1 {
		t.Fatal("supersede-lookup failure branch not found")
	}
	window := source[idx-600 : idx+400]
	if !strings.Contains(window, "doneErrors = append(doneErrors") {
		t.Fatal("supersede-lookup failure no longer recorded in doneErrors")
	}
}

// TestDoneErrorsAreReported: doneErrors must be READ somewhere — it was a
// write-only variable for its entire history (11 append sites, 0 reads),
// which made every "recorded" failure silent.
func TestDoneErrorsAreReported(t *testing.T) {
	source := readDoneSource(t)
	if !strings.Contains(source, "len(doneErrors) > 0") {
		t.Fatal("doneErrors is write-only again — accumulated failures are never surfaced")
	}
}
