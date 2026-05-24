package cmd

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGBrainWorkerLaunchDecisionDisabledDoesNotCallEndpoint(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		t.Fatalf("unexpected GBrain launch decision request: %s", r.URL.String())
	}))
	defer server.Close()

	t.Setenv(envGBrainWorkerLaunchDecisionEnabled, "")
	t.Setenv(envGBrainWorkerLaunchDecisionURL, server.URL)

	if err := enforceGBrainWorkerLaunchDecision(gbrainWorkerLaunchDecisionRequest{
		TargetID:     "gt-123",
		WorkerSystem: "gastown",
	}); err != nil {
		t.Fatalf("enforceGBrainWorkerLaunchDecision() error = %v", err)
	}
	if called {
		t.Fatalf("endpoint should not be called while disabled")
	}
}

func TestGBrainWorkerLaunchDecisionAllowsRepairAction(t *testing.T) {
	var seenActionCode string
	var seenWorkerSystem string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenActionCode = r.URL.Query().Get("action_code")
		seenWorkerSystem = r.URL.Query().Get("worker_system")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{
			"contract":"gbrain.worker_launch_decision.v1",
			"request":{
				"target_id":%q,
				"lane":%q,
				"action_code":%q,
				"worker_system":%q,
				"worker_role":%q
			},
			"decision":{
				"allowed":true,
				"state":"repair_admitted",
				"policy_mode":"repair_only",
				"warning_reason_codes":["repair_action_not_current_next_step"]
			}
		}`,
			r.URL.Query().Get("target_id"),
			r.URL.Query().Get("lane"),
			r.URL.Query().Get("action_code"),
			r.URL.Query().Get("worker_system"),
			r.URL.Query().Get("worker_role"),
		)
	}))
	defer server.Close()

	t.Setenv(envGBrainWorkerLaunchDecisionEnabled, "1")
	t.Setenv(envGBrainWorkerLaunchDecisionURL, server.URL)

	err := enforceGBrainWorkerLaunchDecision(gbrainWorkerLaunchDecisionRequest{
		TargetID:     "gt-123",
		WorkerSystem: "gastown",
		WorkerRole:   "gastown/polecats/_",
		ActionText:   "repair gbrain worker memory coverage",
	})
	if err != nil {
		t.Fatalf("enforceGBrainWorkerLaunchDecision() error = %v", err)
	}
	if seenActionCode != "REPAIR_GBRAIN_WORKER_MEMORY_COVERAGE" {
		t.Fatalf("action_code = %q, want REPAIR_GBRAIN_WORKER_MEMORY_COVERAGE", seenActionCode)
	}
	if seenWorkerSystem != "gastown" {
		t.Fatalf("worker_system = %q, want gastown", seenWorkerSystem)
	}
}

func TestGBrainWorkerLaunchActionCodeFromGranularRepairText(t *testing.T) {
	tests := []struct {
		text string
		want string
	}{
		{
			text: "Action code: BACKFILL_OR_ARCHIVE_ORPHAN_WORKER_RUN",
			want: "REPAIR_GBRAIN_WORKER_MEMORY_COVERAGE",
		},
		{
			text: "Generated from the GBrain worker-memory recovery handoff",
			want: "REPAIR_GBRAIN_WORKER_MEMORY_COVERAGE",
		},
		{
			text: "Action code: CAPTURE_GBRAIN_SOURCE_DOCUMENT",
			want: "CAPTURE_GBRAIN_SOURCE_DOCUMENTS",
		},
		{
			text: "Validate the GBrain autonomy gate",
			want: "VALIDATE_GBRAIN_AUTONOMY_GATE",
		},
		{
			text: "ordinary sales follow-up",
			want: "",
		},
	}

	for _, tt := range tests {
		if got := gbrainWorkerLaunchActionCodeFromText(tt.text); got != tt.want {
			t.Fatalf("gbrainWorkerLaunchActionCodeFromText(%q) = %q, want %q", tt.text, got, tt.want)
		}
	}
}

func TestGBrainWorkerLaunchDecisionDeniesRepairOnlyMissingAction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{
			"contract":"gbrain.worker_launch_decision.v1",
			"request":{
				"target_id":%q,
				"lane":%q,
				"action_code":%q,
				"worker_system":%q,
				"worker_role":%q
			},
			"decision":{
				"allowed":false,
				"state":"denied_by_repair_only_policy",
				"policy_mode":"repair_only",
				"recommendation":"launch_allowed_repair_action_or_wait_for_autonomy_resume",
				"blocking_reason_codes":["repair_only_mode","missing_repair_action_code"],
				"next_action_codes":["REPAIR_GBRAIN_WORKER_MEMORY_COVERAGE"]
			}
		}`,
			r.URL.Query().Get("target_id"),
			r.URL.Query().Get("lane"),
			r.URL.Query().Get("action_code"),
			r.URL.Query().Get("worker_system"),
			r.URL.Query().Get("worker_role"),
		)
	}))
	defer server.Close()

	t.Setenv(envGBrainWorkerLaunchDecisionEnabled, "1")
	t.Setenv(envGBrainWorkerLaunchDecisionURL, server.URL)

	err := enforceGBrainWorkerLaunchDecision(gbrainWorkerLaunchDecisionRequest{
		TargetID:     "gt-123",
		WorkerSystem: "gastown",
	})
	if err == nil {
		t.Fatalf("expected GBrain denial")
	}
	if !strings.Contains(err.Error(), "repair_only_mode") {
		t.Fatalf("error %q does not include repair_only_mode", err.Error())
	}
}

func TestGBrainWorkerLaunchDecisionInvalidResponseFailsClosed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"contract":"wrong","decision":{"allowed":true}}`)
	}))
	defer server.Close()

	t.Setenv(envGBrainWorkerLaunchDecisionEnabled, "1")
	t.Setenv(envGBrainWorkerLaunchDecisionURL, server.URL)

	err := enforceGBrainWorkerLaunchDecision(gbrainWorkerLaunchDecisionRequest{
		TargetID:     "gt-123",
		WorkerSystem: "gastown",
	})
	if err == nil {
		t.Fatalf("expected invalid response to fail closed")
	}
	if !strings.Contains(err.Error(), "gbrain.worker_launch_decision.v1") {
		t.Fatalf("error %q does not name the expected contract", err.Error())
	}
}

func TestGBrainWorkerLaunchDecisionMismatchedResponseFailsClosed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"contract":"gbrain.worker_launch_decision.v1",
			"request":{
				"target_id":"other-bead",
				"lane":"full_autonomy",
				"action_code":"",
				"worker_system":"openclaw",
				"worker_role":"COO"
			},
			"decision":{
				"allowed":true,
				"state":"repair_admitted",
				"policy_mode":"repair_only"
			}
		}`)
	}))
	defer server.Close()

	t.Setenv(envGBrainWorkerLaunchDecisionEnabled, "1")
	t.Setenv(envGBrainWorkerLaunchDecisionURL, server.URL)

	err := enforceGBrainWorkerLaunchDecision(gbrainWorkerLaunchDecisionRequest{
		TargetID:     "gt-123",
		ActionCode:   "REPAIR_GBRAIN_WORKER_MEMORY_COVERAGE",
		WorkerSystem: "gastown",
		WorkerRole:   "gastown/polecats/_",
	})
	if err == nil {
		t.Fatalf("expected mismatched response to fail closed")
	}
	for _, want := range []string{
		"did not match request",
		"target_id: expected gt-123, got other-bead",
		"lane: expected assisted_autonomy, got full_autonomy",
		"action_code: expected REPAIR_GBRAIN_WORKER_MEMORY_COVERAGE, got null",
		"worker_system: expected gastown, got openclaw",
		"worker_role: expected gastown/polecats/_, got COO",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not include %q", err.Error(), want)
		}
	}
}

func TestExecuteSlingStopsBeforeSpawnWhenGBrainDenies(t *testing.T) {
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	routes := strings.Join([]string{
		`{"prefix":"gt-","path":"gastown/mayor/rig"}`,
		`{"prefix":"hq-","path":"."}`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, "gastown", "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir rig: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	_ = writeBDStub(t, binDir, `#!/bin/sh
set -e
if [ "$1" = "show" ]; then
  echo '[{"title":"Normal ops work","status":"open","assignee":"","description":"","labels":[]}]'
  exit 0
fi
exit 0
`, `@echo off
if "%1"=="show" (
  echo [{"title":"Normal ops work","status":"open","assignee":"","description":"","labels":[]}]
  exit /b 0
)
exit /b 0
`)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{
			"contract":"gbrain.worker_launch_decision.v1",
			"request":{
				"target_id":%q,
				"lane":%q,
				"action_code":%q,
				"worker_system":%q,
				"worker_role":%q
			},
			"decision":{
				"allowed":false,
				"state":"denied_by_repair_only_policy",
				"policy_mode":"repair_only",
				"blocking_reason_codes":["repair_only_mode","missing_repair_action_code"]
			}
		}`,
			r.URL.Query().Get("target_id"),
			r.URL.Query().Get("lane"),
			r.URL.Query().Get("action_code"),
			r.URL.Query().Get("worker_system"),
			r.URL.Query().Get("worker_role"),
		)
	}))
	defer server.Close()
	t.Setenv(envGBrainWorkerLaunchDecisionEnabled, "1")
	t.Setenv(envGBrainWorkerLaunchDecisionURL, server.URL)

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(filepath.Join(townRoot, "mayor", "rig")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	spawned := false
	prevSpawn := spawnPolecatForSling
	t.Cleanup(func() { spawnPolecatForSling = prevSpawn })
	spawnPolecatForSling = func(rigName string, opts SlingSpawnOptions) (*SpawnedPolecatInfo, error) {
		spawned = true
		return nil, fmt.Errorf("unexpected spawn")
	}

	_, err = executeSling(SlingParams{
		BeadID:        "gt-abc123",
		RigName:       "gastown",
		TownRoot:      townRoot,
		BeadsDir:      filepath.Join(townRoot, ".beads"),
		NoConvoy:      true,
		NoBoot:        true,
		CallerContext: "test",
	})
	if err == nil {
		t.Fatalf("expected GBrain denial")
	}
	if spawned {
		t.Fatalf("spawnPolecatForSling should not run after GBrain denial")
	}
	if !strings.Contains(err.Error(), "GBrain worker launch decision denied dispatch") {
		t.Fatalf("unexpected error: %v", err)
	}
}
