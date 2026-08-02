package cmd

import (
	"strings"
	"testing"
)

// A blank sender in a mail list is not cosmetic: Gas Town's human-review gate
// accepts "named mail from a real town role" as merge authorisation, so a
// message that renders with no visible sender is indistinguishable from an
// unattributed — or forged — instruction. On 2026-08-01 a merge approval listed
// exactly that way while its from: label was present the whole time.
func TestSenderForDisplayNeverReturnsBlank(t *testing.T) {
	for _, in := range []string{"", " ", "\t", "\n", "   \t\n "} {
		got := senderForDisplay(in)
		if strings.TrimSpace(got) == "" {
			t.Errorf("senderForDisplay(%q) returned blank — a reader cannot tell a display bug from a missing sender", in)
		}
		if got != UnattributedSender {
			t.Errorf("senderForDisplay(%q) = %q, want the explicit unattributed marker", in, got)
		}
	}
}

func TestSenderForDisplayPassesRealSendersThrough(t *testing.T) {
	for _, in := range []string{"mayor/", "overseer", "billing_automation/refinery", "gastown/Toast"} {
		if got := senderForDisplay(in); got != in {
			t.Errorf("senderForDisplay(%q) = %q, want it unchanged", in, got)
		}
	}
}

// The marker must tell the reader what to DO, not merely that something is
// missing — an agent deciding whether to act on an approval needs the check.
func TestUnattributedMarkerNamesTheVerificationStep(t *testing.T) {
	if !strings.Contains(UnattributedSender, "gt show") {
		t.Errorf("unattributed marker should name how to verify the sender, got %q", UnattributedSender)
	}
}
