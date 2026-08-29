package daemon

import (
	"os"
	"strings"
	"testing"
)

// Source-pattern guards (same style as the reaper's agent-bead guards):
// the three Deacon-lifecycle sites must stay pause-aware. A deliberately
// paused Deacon writes no heartbeat BY DESIGN, so the staleness handler
// killing/respawning it would override a human stop. (renascentia 2026-08-29)
func TestDaemonDeaconLifecycleIsPauseAware(t *testing.T) {
	data, err := os.ReadFile("daemon.go")
	if err != nil {
		t.Fatalf("read daemon.go: %v", err)
	}
	source := string(data)

	for _, fn := range []string{"func (d *Daemon) checkDeaconHeartbeat()", "func (d *Daemon) ensureDeaconRunning()"} {
		start := strings.Index(source, fn)
		if start == -1 {
			t.Fatalf("could not locate %s", fn)
		}
		body := source[start:]
		if end := strings.Index(body[1:], "\nfunc "); end != -1 {
			body = body[:end+1]
		}
		if !strings.Contains(body, "deacon.IsPaused") {
			t.Fatalf("%s lost its pause guard — the daemon would kill/respawn a deliberately paused Deacon", fn)
		}
	}

	// The queued-work dispatch step must early-out on pause.
	stepIdx := strings.Index(source, "skipping queued-work dispatch")
	if stepIdx == -1 {
		t.Fatal("heartbeat step 14 lost its deacon-pause early-out")
	}
}
