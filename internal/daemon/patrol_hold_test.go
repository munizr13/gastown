package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"runtime"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/deacon"
)

func TestPatrolHoldDoesNotDisableTimerRegistration(t *testing.T) {
	var logs bytes.Buffer
	d := &Daemon{config: &Config{TownRoot: t.TempDir()}, logger: log.New(&logs, "", 0)}
	if err := deacon.Pause(d.config.TownRoot, "incident", "human"); err != nil {
		t.Fatal(err)
	}
	if !d.isPatrolActive("witness") {
		t.Fatal("pause must not disable timer registration")
	}
	if d.canRunPatrol("witness") {
		t.Fatal("paused patrol executed")
	}
	if err := deacon.Resume(d.config.TownRoot); err != nil {
		t.Fatal(err)
	}
	if !d.canRunPatrol("witness") {
		t.Fatal("resume must take effect without restarting daemon")
	}
}

func TestHeldReaperCannotDispatchOrFallBackInline(t *testing.T) {
	var logs bytes.Buffer
	d := &Daemon{config: &Config{TownRoot: t.TempDir()}, logger: log.New(&logs, "", 0)}
	if err := deacon.Pause(d.config.TownRoot, "incident", "human"); err != nil {
		t.Fatal(err)
	}
	// Invalid executable would produce a different error if dispatch ran.
	if err := d.dispatchReaperDog(nil); !errors.Is(err, deacon.ErrPatrolHeld) {
		t.Fatalf("dispatch = %v", err)
	}
	// Nil operation dependencies must never be dereferenced after a hold.
	d.reapWispsInline(nil, time.Hour, time.Hour, nil)
	if !bytes.Contains(logs.Bytes(), []byte("inline fallback skipped")) {
		t.Fatalf("missing hold receipt: %s", logs.String())
	}
}

func TestEveryScheduledMaintenanceEntryHonorsHold(t *testing.T) {
	var logs bytes.Buffer
	d := &Daemon{config: &Config{TownRoot: t.TempDir()}, logger: log.New(&logs, "", 0)}
	patrols := map[string]func(){
		"wisp_reaper":           d.reapWisps,
		"checkpoint_dog":        d.runCheckpointDog,
		"compactor_dog":         d.runCompactorDog,
		"doctor_dog":            d.runDoctorDog,
		"dolt_backup":           d.syncDoltBackups,
		"dolt_remotes":          d.pushDoltRemotes,
		"jsonl_git_backup":      d.syncJsonlGitBackup,
		"scheduled_maintenance": d.runScheduledMaintenance,
		"main_branch_test":      d.runMainBranchTests,
		"quota_dog":             d.runQuotaDog,
	}
	if runtime.GOOS != "darwin" {
		delete(patrols, "dolt_backup")
	}
	enabled := map[string]map[string]bool{}
	for name := range patrols {
		enabled[name] = map[string]bool{"enabled": true}
	}
	data, err := json.Marshal(map[string]any{"patrols": enabled})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &d.patrolConfig); err != nil {
		t.Fatal(err)
	}
	if err := deacon.Pause(d.config.TownRoot, "incident", "human"); err != nil {
		t.Fatal(err)
	}
	for name, run := range patrols {
		t.Run(name, func(t *testing.T) {
			logs.Reset()
			if !d.isPatrolActive(name) {
				t.Fatal("test patrol must be enabled in configuration")
			}
			run()
			if !bytes.Contains(logs.Bytes(), []byte(name+": skipping: patrol held")) {
				t.Fatalf("missing hold decision: %s", logs.String())
			}
		})
	}
	// Heartbeat must not restart witnesses/refineries or clean up sessions either.
	logs.Reset()
	d.heartbeat(nil)
	if !bytes.Contains(logs.Bytes(), []byte("Skipping agent management: patrol held")) {
		t.Fatalf("missing heartbeat hold: %s", logs.String())
	}
}
