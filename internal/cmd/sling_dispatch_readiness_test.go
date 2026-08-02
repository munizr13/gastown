package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

func TestValidateBeadDispatchReadyRejectsBlockedStatus(t *testing.T) {
	t.Parallel()

	err := validateBeadDispatchReady("gt-blocked", &beadInfo{Status: "blocked"})
	if err == nil {
		t.Fatal("expected blocked bead to be rejected")
	}
	if !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("error %q should mention blocked status", err.Error())
	}
}

func TestValidateBeadDispatchReadyRejectsOpenBlockingDependency(t *testing.T) {
	t.Parallel()

	info := &beadInfo{
		Status: "open",
		Dependencies: []beads.IssueDep{
			{ID: "gt-parent", Status: "open", DependencyType: "parent-child"},
			{ID: "gt-blocker", Status: "open", DependencyType: "blocks"},
		},
	}

	err := validateBeadDispatchReady("gt-child", info)
	if err == nil {
		t.Fatal("expected open blocking dependency to be rejected")
	}
	if !strings.Contains(err.Error(), "gt-blocker") {
		t.Fatalf("error %q should name blocking dependency", err.Error())
	}
}

func TestValidateBeadDispatchReadyAllowsClosedBlockingDependency(t *testing.T) {
	t.Parallel()

	info := &beadInfo{
		Status: "open",
		Dependencies: []beads.IssueDep{
			{ID: "gt-blocker", Status: "closed", DependencyType: "blocks"},
		},
	}

	if err := validateBeadDispatchReady("gt-child", info); err != nil {
		t.Fatalf("closed blocking dependency should not block dispatch: %v", err)
	}
}

func TestValidateBeadDispatchReadyRejectsMergeBlocksDependency(t *testing.T) {
	t.Parallel()

	info := &beadInfo{
		Status: "open",
		Dependencies: []beads.IssueDep{
			{ID: "gt-merge", Status: "open", DependencyType: "merge-blocks"},
		},
	}

	err := validateBeadDispatchReady("gt-child", info)
	if err == nil {
		t.Fatal("expected open merge-blocks dependency to be rejected")
	}
	if !strings.Contains(err.Error(), "merge-blocks") {
		t.Fatalf("error %q should mention merge-blocks", err.Error())
	}
}

func TestExecuteSlingRejectsBlockedBeadBeforeSpawn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping shell stub test on windows")
	}

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	writeBDStub(t, binDir, `#!/bin/sh
case "$1" in
  show)
    echo '[{"title":"Blocked task","status":"blocked","assignee":"","description":"","dependencies":[]}]'
    ;;
esac
exit 0
`, "")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	result, err := executeSling(SlingParams{
		BeadID:   "test-blocked1",
		RigName:  "testrig",
		TownRoot: townRoot,
	})
	if err == nil {
		t.Fatal("expected executeSling to reject blocked bead")
	}
	if result == nil || result.ErrMsg != "blocked" {
		t.Fatalf("ErrMsg = %#v, want blocked", result)
	}
	if !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("error %q should mention blocked", err.Error())
	}
}
