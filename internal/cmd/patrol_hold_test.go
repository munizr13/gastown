package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/boot"
	"github.com/steveyegge/gastown/internal/deacon"
	"github.com/steveyegge/gastown/internal/witness"
)

func TestBootTriageHeldUsesManagedTownBeforeSessionOrWarrantActions(t *testing.T) {
	town := setupTestTownForConfig(t)
	if err := deacon.Pause(town, "hold triage", "human"); err != nil {
		t.Fatal(err)
	}
	// Deliberately outside the managed town: the Boot object owns the boundary.
	t.Chdir(t.TempDir())
	action, target, err := runDegradedTriage(boot.New(town))
	if err != nil || action != "nothing" || !strings.Contains(target, "hold triage") {
		t.Fatalf("triage = %q, %q, %v", action, target, err)
	}
}

func TestSpawnBudgetBlocksForcedAndNormalPathsBeforeHealthOrAllocation(t *testing.T) {
	for _, force := range []bool{false, true} {
		for _, skipAdmission := range []bool{false, true} {
			town := t.TempDir()
			for i := 0; i < 3; i++ {
				if _, err := witness.ReserveBeadRespawn(town, "test-work"); err != nil {
					t.Fatal(err)
				}
			}
			// No rig config, database or tmux exists. Reaching those boundaries
			// would return a different error or create allocation state.
			info, err := SpawnPolecatForSling("synthetic", SlingSpawnOptions{
				TownRoot: town, HookBead: "test-work", Force: force, SkipAdmission: skipAdmission,
			})
			if info != nil || err == nil || !strings.Contains(err.Error(), "startup admission: respawn budget") {
				t.Fatalf("force=%t skipAdmission=%t: info=%v err=%v", force, skipAdmission, info, err)
			}
			if _, err := os.Stat(filepath.Join(town, "synthetic")); !os.IsNotExist(err) {
				t.Fatal("exhausted budget allocated rig state")
			}
		}
	}
}

func TestRespawnResetRequiresExplicitContainment(t *testing.T) {
	for _, mode := range []string{"active", "paused", "estop", "corrupt"} {
		t.Run(mode, func(t *testing.T) {
			town := setupTestTownForConfig(t)
			t.Chdir(town)
			for i := 0; i < 3; i++ {
				if _, err := witness.ReserveBeadRespawn(town, "test-work"); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "paused":
				if err := deacon.Pause(town, "repair startup", "human"); err != nil {
					t.Fatal(err)
				}
			case "estop":
				if err := os.WriteFile(filepath.Join(town, "ESTOP"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "corrupt":
				path := deacon.GetPauseFile(town)
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := runSlingRespawnReset(nil, []string{"test-work"})
			allowed := mode == "paused" || mode == "estop"
			if (err == nil) != allowed {
				t.Fatalf("mode=%s err=%v", mode, err)
			}
			if witness.ShouldBlockRespawn(town, "test-work") == allowed {
				t.Fatalf("mode=%s: wrong persisted budget after reset", mode)
			}
			if allowed && dispatchFreezeReason(town) == "" {
				t.Fatal("reset silently released containment")
			}
		})
	}
}

func TestMaintenanceHelpersRejectHoldBeforeAccessingDatabase(t *testing.T) {
	town := setupTestTownForConfig(t)
	t.Chdir(town)
	if err := deacon.Pause(town, "hold maintenance", "human"); err != nil {
		t.Fatal(err)
	}
	oldCompact, oldReaper := compactDryRun, reaperDryRun
	compactDryRun, reaperDryRun = false, false
	t.Cleanup(func() { compactDryRun, reaperDryRun = oldCompact, oldReaper })
	for name, run := range map[string]func() error{
		"compact":      func() error { return runCompact(nil, nil) },
		"maintain":     func() error { return runMaintain(nil, nil) },
		"dog dispatch": func() error { _, err := DispatchToDog("alpha", DogDispatchOptions{}); return err },
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(); err == nil || !strings.Contains(err.Error(), "hold maintenance") {
				t.Fatalf("guard = %v", err)
			}
		})
	}
	for _, cmd := range []*cobra.Command{reaperReapCmd, reaperPurgeCmd, reaperAutoCloseCmd, reaperRunCmd} {
		if cmd.PreRunE == nil {
			t.Fatalf("%s has no mutation guard", cmd.Name())
		}
		if err := cmd.PreRunE(cmd, nil); err == nil || !strings.Contains(err.Error(), "hold maintenance") {
			t.Fatalf("%s: %v", cmd.Name(), err)
		}
	}
	reaperDryRun = true
	if err := guardReaperMutation(reaperRunCmd, nil); err != nil {
		t.Fatalf("read-only preview should remain available: %v", err)
	}
}

func TestDailyDigestDryRunPropagatesToCompaction(t *testing.T) {
	town := setupTestTownForConfig(t)
	t.Chdir(town)
	bin := t.TempDir()
	logPath := filepath.Join(bin, "calls")
	t.Setenv("REPAIR_TEST_CALLS", logPath)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for name, script := range map[string]string{
		"bd": "#!/bin/sh\necho bd \"$@\" >> \"$REPAIR_TEST_CALLS\"\necho '[]'\n",
		"gt": "#!/bin/sh\necho gt \"$@\" >> \"$REPAIR_TEST_CALLS\"\necho '{}'\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0755); err != nil {
			t.Fatal(err)
		}
	}
	oldDry, oldJSON, oldDate := compactReportDryRun, compactReportJSON, compactReportDate
	compactReportDryRun, compactReportJSON, compactReportDate = true, true, "2026-09-03"
	t.Cleanup(func() { compactReportDryRun, compactReportJSON, compactReportDate = oldDry, oldJSON, oldDate })
	if err := runDailyDigest(); err != nil {
		t.Fatal(err)
	}
	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(calls), "gt compact --json --dry-run\n") {
		t.Fatalf("compaction was not a preview: %s", calls)
	}
	for _, mutation := range []string{"bd create", "bd close", "gt mail"} {
		if strings.Contains(string(calls), mutation) {
			t.Fatalf("dry run performed %s: %s", mutation, calls)
		}
	}
}
