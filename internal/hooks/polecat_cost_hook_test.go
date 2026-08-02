package hooks

import (
	"strings"
	"testing"
)

// Polecats are the role that does the actual work, and they were the one role
// absent from `gt costs`: every other role records cost on Stop, but polecats
// spent their single Stop slot on the idle-catcher alone. On 2026-08-01 the
// by-role rows summed to exactly the reported town total with no polecat line,
// so two completed pilot runs were invisible to the ledger.
func TestPolecatStopHookRecordsCost(t *testing.T) {
	overrides := DefaultOverrides()
	polecats, ok := overrides["polecats"]
	if !ok {
		t.Fatal("no polecats override defined")
	}
	if len(polecats.Stop) == 0 {
		t.Fatal("polecats have no Stop hook")
	}

	var commands []string
	for _, entry := range polecats.Stop {
		for _, h := range entry.Hooks {
			commands = append(commands, h.Command)
		}
	}
	joined := strings.Join(commands, " | ")

	if !strings.Contains(joined, "costs record") {
		t.Errorf("polecat Stop hook does not record cost; commands: %s", joined)
	}

	// The idle-catcher must survive alongside it. Replacing rather than adding
	// is what created the #3648 non-convergence loop: hooks-sync wrote
	// polecat-stop-check, doctor demanded costs record, the fix deleted the
	// file, the daemon recreated it, forever.
	if !strings.Contains(joined, "polecat-stop-check") {
		t.Errorf("polecat Stop hook lost the idle-catcher; commands: %s", joined)
	}
}

// Cost accounting must never delay session teardown — the other roles'
// templates background it, and polecats should match.
func TestPolecatCostRecordIsBackgrounded(t *testing.T) {
	for _, entry := range DefaultOverrides()["polecats"].Stop {
		for _, h := range entry.Hooks {
			if strings.Contains(h.Command, "costs record") && !strings.Contains(h.Command, "&") {
				t.Errorf("polecat cost recording should be backgrounded, got %q", h.Command)
			}
		}
	}
}
