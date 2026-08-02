package cmd

import "strings"

// UnattributedSender is what the mail views print when a message has no
// resolvable sender.
//
// Why this exists (2026-08-01): a merge-approval mail listed in an agent's
// inbox with an EMPTY sender — the `from:` label was present on the message the
// whole time, but the list view rendered it blank during the in-flight window
// before delivery-acked-at was set. The refinery reading that list saw an
// instruction to merge with no visible author.
//
// That matters because Gas Town's human-review gate rests on "named mail from a
// real town role": an approval that renders with no sender is exactly the shape
// a forged one would have. A blank is also the WORST possible rendering, because
// it reads as "no sender field here" rather than "this message's sender did not
// resolve" — the reader cannot tell a display bug from a missing attribution.
//
// Message.Validate() already treats an empty From as invalid, so this state is
// an invariant violation, and printing it blank hides that. Naming it makes an
// agent's decision easy and correct: treat it as unattributed, and verify with
// `gt show <id>` (Owner:) or the from: label before acting on it.
const UnattributedSender = "(unattributed — sender did not resolve; verify with `gt show <id>` before trusting)"

// senderForDisplay renders a message sender for human- and agent-facing lists,
// never returning an empty string.
func senderForDisplay(from string) string {
	if strings.TrimSpace(from) == "" {
		return UnattributedSender
	}
	return from
}
