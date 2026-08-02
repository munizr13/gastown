package refinery

import (
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

// MR beads are created Ephemeral, which routes them to the wisps table
// (GH#2446), and wisps are compacted away. On 2026-08-01 an MR bead carrying a
// witness review, a refinery hold and the rulings that authorised the merge was
// readable 39 minutes after the merge and gone afterwards — "why was this
// merged?" became unanswerable. closeMRWithReason now mirrors that record onto
// the durable source issue before closing. These tests hold that line.

func TestCloseMRWithReasonMirrorsReviewRecordOntoSourceIssue(t *testing.T) {
	e, b, mrIssue, agentIssue, srcIssue := setupEngineerTerminalCloseTest(t, "gt-wisp-old")

	// The kind of content that was lost: a witness review and a hold reason.
	witnessNote := "WITNESS REVIEW: 43/43 tests pass, boundaries held."
	holdNote := "HELD BY REFINERY — waiting for named approval from the overseer."
	for _, c := range []string{witnessNote, holdNote} {
		if err := b.AddComment(mrIssue.ID, c); err != nil {
			t.Fatalf("seed MR comment: %v", err)
		}
	}

	// These values must match what the harness seeded on the MR bead: for a
	// merged close, closeTerminalMR re-verifies the MR against the recorded
	// merge proof and refuses if anything drifted. That guard is correct — the
	// test has to describe the real MR, not an invented one.
	err := e.closeMRWithReason(&MRInfo{
		ID:          mrIssue.ID,
		AgentBead:   agentIssue.ID,
		SourceIssue: srcIssue.ID,
		Branch:      "polecat/test/gt-xyz",
		Target:      "main",
		Worker:      "test",
	}, string(CloseReasonMerged), "abc123")
	if err != nil {
		t.Fatalf("closeMRWithReason: %v", err)
	}

	// The MR itself is closed and will eventually be compacted away...
	assertIssueStatus(t, b, mrIssue.ID, string(beads.StatusClosed))

	// ...but the record must now exist on the durable source issue.
	comments, err := b.Comments(srcIssue.ID)
	if err != nil {
		t.Fatalf("read source issue comments: %v", err)
	}
	var mirrored string
	for _, c := range comments {
		if strings.Contains(c.Text, "MERGE RECORD") {
			mirrored = c.Text
			break
		}
	}
	if mirrored == "" {
		t.Fatalf("no MERGE RECORD mirrored onto source issue %s; comments=%d", srcIssue.ID, len(comments))
	}

	for _, want := range []string{
		witnessNote,           // the review survived
		holdNote,              // the hold reasoning survived
		mrIssue.ID,            // provenance: which MR it came from
		"abc123",              // the merge commit
		"polecat/test/gt-xyz", // the branch
		"test",                // the worker
		string(CloseReasonMerged),
	} {
		if !strings.Contains(mirrored, want) {
			t.Errorf("mirrored record missing %q:\n%s", want, mirrored)
		}
	}
}

// A merge must never fail because the audit mirror could not be written. The
// mirror is evidence, not a gate.
func TestCloseMRWithReasonStillClosesWhenThereIsNoSourceIssue(t *testing.T) {
	e, b, mrIssue, agentIssue, _ := setupEngineerTerminalCloseTest(t, "gt-wisp-old")

	// SourceIssue deliberately empty — nothing durable to mirror onto.
	if err := e.closeMRWithReason(&MRInfo{
		ID:        mrIssue.ID,
		AgentBead: agentIssue.ID,
	}, string(CloseReasonMerged), "abc123"); err != nil {
		t.Fatalf("closeMRWithReason must not fail when there is no source issue: %v", err)
	}

	assertIssueStatus(t, b, mrIssue.ID, string(beads.StatusClosed))
	assertMRCloseReason(t, b, mrIssue.ID, string(CloseReasonMerged))
}

// Same guarantee when the source issue ID does not resolve: report, don't block.
func TestCloseMRWithReasonStillClosesWhenSourceIssueIsUnresolvable(t *testing.T) {
	e, b, mrIssue, agentIssue, _ := setupEngineerTerminalCloseTest(t, "gt-wisp-old")

	// A rejected close carries no merge proof, so the drift guard does not
	// apply and we can exercise a source issue that does not resolve.
	if err := e.closeMRWithReason(&MRInfo{
		ID:          mrIssue.ID,
		AgentBead:   agentIssue.ID,
		SourceIssue: "gt-does-not-exist",
	}, "rejected: policy failed"); err != nil {
		t.Fatalf("closeMRWithReason must not fail on an unresolvable source issue: %v", err)
	}

	assertIssueStatus(t, b, mrIssue.ID, string(beads.StatusClosed))
}
