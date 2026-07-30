package beads

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestListMergeRequestsFailsClosedOnWispsQueryError is the regression test for
// the RENASCENTIA double-submit fix (2026-07-30, approval a5cfeced under
// GASTOWN-CORE-CHANGE-PROTOCOL v1).
//
// MR beads are created Ephemeral, so they live ONLY in the wisps table. The
// original code ran the wisps query and discarded its error:
//
//	sqlOut, sqlErr := b.run("sql", "--json", query)
//	if sqlErr == nil && len(sqlOut) > 0 && isJSONBytes(sqlOut) { ... }
//	return issueResults, nil   // ← error swallowed
//
// A failed wisps query therefore returned an EMPTY-but-successful list, which
// made FindMRForBranchAndSHA report "no existing MR" (so `gt done` created a
// second one) and FindOpenMRsForIssue report "no old MRs" (so the first one
// was never superseded) — two open MRs per commit.
//
// The list must now fail closed: callers have to be able to distinguish
// "there are no MRs" from "I could not read the MRs".
func TestListMergeRequestsFailsClosedOnWispsQueryError(t *testing.T) {
	// Fake bd: `list` succeeds with an empty JSON array (issues table has no
	// MRs — the normal case, since MRs are wisps), `sql` fails (wisps table
	// unreadable), `version` satisfies any preflight.
	dir := t.TempDir()
	script := `#!/bin/sh
for arg in "$@"; do
  case "$arg" in
    sql) echo "fatal: dolt: connection refused" >&2; exit 1 ;;
    version) echo "bd version 1.0.4"; exit 0 ;;
    list) echo "[]"; exit 0 ;;
  esac
done
echo "[]"
exit 0
`
	fake := filepath.Join(dir, "bd")
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	b := NewIsolated(t.TempDir())
	issues, err := b.ListMergeRequests(ListOptions{Status: "all", Label: "gt:merge-request"})
	if err == nil {
		t.Fatalf("expected an error when the wisps query fails, got nil (issues=%d). "+
			"A silent empty list is exactly what caused the double-submit bug.", len(issues))
	}
	if issues != nil {
		t.Errorf("expected nil issues alongside the error, got %d", len(issues))
	}
	if !strings.Contains(err.Error(), "incomplete") {
		t.Errorf("error should tell the caller the list would be incomplete, got: %v", err)
	}
}
