package daemon

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/deacon"
)

func reaperReceiptFixture(t *testing.T) (*Daemon, *dogMol, string) {
	t.Helper()
	dir := t.TempDir()
	bd, receipts := filepath.Join(dir, "bd"), filepath.Join(dir, "receipts")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$*\" >> %q\n", receipts)
	if err := os.WriteFile(bd, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{config: &Config{TownRoot: dir}, logger: log.New(io.Discard, "", 0)}
	mol := &dogMol{rootID: "fixture-root", bdPath: bd, townRoot: dir, logger: d.logger,
		stepIDs: map[string]string{"scan": "scan", "reap": "reap", "purge": "purge", "auto-close": "auto-close", "report": "report"}}
	return d, mol, receipts
}

func TestInlineReaperInvalidDatabaseCannotClaimExecution(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		t.Run(fmt.Sprint(dryRun), func(t *testing.T) {
			d, mol, receiptFile := reaperReceiptFixture(t)
			d.reapWispsInline(&WispReaperConfig{Databases: []string{"invalid database"}, DryRun: dryRun}, time.Hour, time.Hour, mol)
			data, err := os.ReadFile(receiptFile)
			if err != nil {
				t.Fatal(err)
			}
			receipts := string(data)
			for _, step := range []string{"reap", "purge", "auto-close"} {
				want := fmt.Sprintf("close %s --reason failed: %s: dry_run=%t, databases_returned=0, schema_skipped=0, errors=1", step, step, dryRun)
				if !strings.Contains(receipts, want) {
					t.Fatalf("missing failure for %s: %s", step, receipts)
				}
			}
			for _, phase := range []string{"plugin_receipts", "plugin_dispatches"} {
				if !strings.Contains(receipts, phase+"={databases_returned=0, schema_skipped=0, errors=1}") {
					t.Fatalf("unreported auxiliary failure: %s", receipts)
				}
			}
		})
	}
}

func TestReaperReceiptDistinguishesSkippedAndPartialOperationResults(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stats reaperPhaseStats
		want  string
	}{
		{"all-skipped", reaperPhaseStats{skipped: 2}, "skipped: no database operation returned; purge: dry_run=true, databases_returned=0, schema_skipped=2, errors=0"},
		{"partial-failure", reaperPhaseStats{returned: 1, errors: 1}, "failed: purge: dry_run=true, databases_returned=1, schema_skipped=0, errors=1"},
		{"scoped-success", reaperPhaseStats{returned: 1, skipped: 1}, "returned: purge: dry_run=true, databases_returned=1, schema_skipped=1, errors=0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, mol, receiptFile := reaperReceiptFixture(t)
			tc.stats.record(mol, "purge", true, "wisps=0, mail=0")
			data, err := os.ReadFile(receiptFile)
			if err != nil || !strings.Contains(string(data), tc.want) {
				t.Fatalf("incorrect execution receipt: %s (%v)", data, err)
			}
		})
	}
}

func TestReaperDatabaseGateRechecksHoldBeforeConnection(t *testing.T) {
	d, _, _ := reaperReceiptFixture(t)
	if err := deacon.Pause(d.config.TownRoot, "fixture hold", "test"); err != nil {
		t.Fatal(err)
	}
	if db, err := d.openReaperDatabase("fixture", time.Second); db != nil || !errors.Is(err, deacon.ErrPatrolHeld) {
		t.Fatalf("held database admitted: %v %v", db, err)
	}
}

func TestWispReaperInterval(t *testing.T) {
	// Default (now 1h after Dog-driven refactor)
	if got := wispReaperInterval(nil); got != defaultWispReaperInterval {
		t.Errorf("expected default %v, got %v", defaultWispReaperInterval, got)
	}

	// Custom
	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			WispReaper: &WispReaperConfig{
				Enabled:     true,
				IntervalStr: "2h",
			},
		},
	}
	if got := wispReaperInterval(config); got != 2*time.Hour {
		t.Errorf("expected 2h, got %v", got)
	}

	// Invalid falls back to default
	config.Patrols.WispReaper.IntervalStr = "nope"
	if got := wispReaperInterval(config); got != defaultWispReaperInterval {
		t.Errorf("expected default for invalid, got %v", got)
	}
}

func TestWispReaperMaxAge(t *testing.T) {
	if got := wispReaperMaxAge(nil); got != defaultWispMaxAge {
		t.Errorf("expected default %v, got %v", defaultWispMaxAge, got)
	}

	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			WispReaper: &WispReaperConfig{
				Enabled:   true,
				MaxAgeStr: "48h",
			},
		},
	}
	if got := wispReaperMaxAge(config); got != 48*time.Hour {
		t.Errorf("expected 48h, got %v", got)
	}
}

func TestWispDeleteAge(t *testing.T) {
	if got := wispDeleteAge(nil); got != defaultWispDeleteAge {
		t.Errorf("expected default %v, got %v", defaultWispDeleteAge, got)
	}

	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			WispReaper: &WispReaperConfig{
				Enabled:      true,
				DeleteAgeStr: "336h",
			},
		},
	}
	if got := wispDeleteAge(config); got != 14*24*time.Hour {
		t.Errorf("expected 336h, got %v", got)
	}
}

func TestDefaultReaperIntervalIsOneHour(t *testing.T) {
	// Verify the default changed from 30m to 1h per issue gt-caf7.
	if defaultWispReaperInterval != 1*time.Hour {
		t.Errorf("expected default interval 1h, got %v", defaultWispReaperInterval)
	}
}

func TestDispatchReaperDogUsesDogPoolSling(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mock")
	}

	townRoot := t.TempDir()
	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "gt-args.log")
	fakeGT := filepath.Join(binDir, "gt")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %q\n", logPath)
	if err := os.WriteFile(fakeGT, []byte(script), 0755); err != nil {
		t.Fatalf("write fake gt: %v", err)
	}

	d := &Daemon{
		config: &Config{TownRoot: townRoot},
		gtPath: fakeGT,
	}
	if err := d.dispatchReaperDog(map[string]string{"max_age": "1h"}); err != nil {
		t.Fatalf("dispatchReaperDog() error = %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read gt args log: %v", err)
	}
	args := strings.Split(strings.TrimSpace(string(data)), "\n")
	wantPrefix := []string{"sling", constants.MolDogReaper, "deacon/dogs"}
	if len(args) < len(wantPrefix) {
		t.Fatalf("gt args = %v, want prefix %v", args, wantPrefix)
	}
	for i, want := range wantPrefix {
		if args[i] != want {
			t.Fatalf("gt arg %d = %q, want %q (all args: %v)", i, args[i], want, args)
		}
	}
}

func TestSuccessfulReaperDispatchDoesNotCreateOrCloseExecutionSteps(t *testing.T) {
	dir := t.TempDir()
	fakeGT := filepath.Join(dir, "gt")
	fakeBD := filepath.Join(dir, "bd")
	marker := filepath.Join(dir, "duplicate-molecule")
	if err := os.WriteFile(fakeGT, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fakeBD, []byte("#!/bin/sh\necho called > '"+marker+"'\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{config: &Config{TownRoot: dir}, gtPath: fakeGT, bdPath: fakeBD, logger: log.New(io.Discard, "", 0),
		patrolConfig: &DaemonPatrolConfig{Patrols: &PatrolsConfig{WispReaper: &WispReaperConfig{Enabled: true}}}}
	d.reapWisps()
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("dispatch created a duplicate execution molecule")
	}
}

func TestReaperDispatchDistinguishesNotStartedFromUnknownOutcome(t *testing.T) {
	dir := t.TempDir()
	d := &Daemon{config: &Config{TownRoot: dir}, gtPath: filepath.Join(dir, "missing-gt")}
	if err := d.dispatchReaperDog(nil); !errors.Is(err, errReaperDispatchNotStarted) {
		t.Fatalf("missing executable must establish not-started: %v", err)
	}
	for _, ending := range []string{"exit 1", "kill -TERM $$"} {
		t.Run(ending, func(t *testing.T) {
			dir := t.TempDir()
			gt, bd := filepath.Join(dir, "gt"), filepath.Join(dir, "bd")
			started, fallback := filepath.Join(dir, "started"), filepath.Join(dir, "fallback")
			if err := os.WriteFile(gt, []byte(fmt.Sprintf("#!/bin/sh\ntouch %q\n%s\n", started, ending)), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(bd, []byte(fmt.Sprintf("#!/bin/sh\ntouch %q\nexit 1\n", fallback)), 0755); err != nil {
				t.Fatal(err)
			}
			var logs bytes.Buffer
			d := &Daemon{config: &Config{TownRoot: dir}, gtPath: gt, bdPath: bd, logger: log.New(&logs, "", 0),
				patrolConfig: &DaemonPatrolConfig{Patrols: &PatrolsConfig{WispReaper: &WispReaperConfig{
					Enabled: true, Databases: []string{"invalid database name"},
				}}}}
			d.reapWisps()
			if _, err := os.Stat(started); err != nil {
				t.Fatal("test did not reach the started dispatch")
			}
			if _, err := os.Stat(fallback); !os.IsNotExist(err) {
				t.Fatal("uncertain dispatch triggered a second execution path")
			}
			if !strings.Contains(logs.String(), "dispatch outcome unconfirmed") {
				t.Fatalf("uncertainty was hidden: %s", logs.String())
			}
		})
	}
}

func TestDoltServerHostIgnoresStaleBeadsHost(t *testing.T) {
	t.Setenv("GT_DOLT_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "stale-host")

	d := &Daemon{config: &Config{TownRoot: t.TempDir()}}
	if got := d.doltServerHost(); got != "127.0.0.1" {
		t.Fatalf("doltServerHost() = %q, want default localhost", got)
	}
}

func TestDoltServerHostUsesConfiguredTownHost(t *testing.T) {
	t.Setenv("GT_DOLT_IGNORE_CONFIG", "")
	t.Setenv("GT_DOLT_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "stale-host")
	townRoot := t.TempDir()
	doltDataDir := filepath.Join(townRoot, ".dolt-data")
	if err := os.MkdirAll(doltDataDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(doltDataDir, "config.yaml"), []byte("listener:\n  host: 127.0.0.2\n  port: 5507\n"), 0644); err != nil {
		t.Fatal(err)
	}

	d := &Daemon{config: &Config{TownRoot: townRoot}}
	if got := d.doltServerHost(); got != "127.0.0.2" {
		t.Fatalf("doltServerHost() = %q, want configured host", got)
	}
}
