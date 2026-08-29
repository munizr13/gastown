package web

import (
	"testing"
	"time"
)

// Stored timestamps are RFC3339 UTC; the dashboard renders for a human in
// local time. This pins the .Local() conversion in formatTimestamp with a
// deterministic zone regardless of the machine's TZ — mutation-verifiable:
// removing the conversion makes a UTC afternoon render two hours early in
// Madrid (renascentia 2026-08-29).
func TestFormatTimestampRendersLocalTime(t *testing.T) {
	madrid, err := time.LoadLocation("Europe/Madrid")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	orig := time.Local
	time.Local = madrid
	defer func() { time.Local = orig }()

	// 2026-08-29T16:11:00Z == 18:11 CEST — same year as Now() is not
	// guaranteed forever, so accept either format but pin the CLOCK.
	utc := time.Date(2026, 8, 29, 16, 11, 0, 0, time.UTC)
	got := formatTimestamp(utc)
	if got != "Aug 29, 6:11 PM" && got != "Aug 29 2026, 6:11 PM" {
		t.Fatalf("formatTimestamp rendered %q — expected the 6:11 PM CEST wall clock, not raw UTC", got)
	}
}
