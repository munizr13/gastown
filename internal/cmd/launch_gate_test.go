package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLaunchGateAllowsUnmarkedRig(t *testing.T) {
	townRoot := t.TempDir()
	err := enforceFromScratchLaunchGate(townRoot, "alpha", "alpha-123", &beadInfo{Title: "Implement API"}, launchGateDispatchOptions{})
	if err != nil {
		t.Fatalf("unmarked rig should not require launch gate: %v", err)
	}
}

func TestLaunchGateBlocksMarkedImplementationWithoutReceipt(t *testing.T) {
	townRoot := t.TempDir()
	writeLaunchGateMarker(t, townRoot, "alpha")

	err := enforceFromScratchLaunchGate(townRoot, "alpha", "alpha-123", &beadInfo{Title: "Implement API"}, launchGateDispatchOptions{})
	if err == nil {
		t.Fatal("marked from-scratch rig should block implementation without receipt or override")
	}
	msg := err.Error()
	for _, want := range []string{"from-scratch project launch gate blocked", launchGateReceiptRelPath, launchGateOverrideRelPath} {
		if !strings.Contains(msg, want) {
			t.Fatalf("block error missing %q:\n%s", want, msg)
		}
	}
}

func TestLaunchGateAllowsPlanningFormulaBeforeReceipt(t *testing.T) {
	townRoot := t.TempDir()
	writeLaunchGateMarker(t, townRoot, "alpha")

	err := enforceFromScratchLaunchGate(townRoot, "alpha", "mol-idea-to-plan", nil, launchGateDispatchOptions{
		FormulaName: "mol-idea-to-plan",
	})
	if err != nil {
		t.Fatalf("planning formula should be allowed before receipt: %v", err)
	}
}

func TestLaunchGateAllowsExplicitOverride(t *testing.T) {
	townRoot := t.TempDir()
	repoRoot := writeLaunchGateMarker(t, townRoot, "alpha")
	writeLaunchGateJSON(t, filepath.Join(repoRoot, launchGateOverrideRelPath), map[string]any{
		"contract":          launchGateOverrideContract,
		"decision":          "allow_implementation_without_mol_idea_to_plan",
		"paperclip_issue":   "RENA-123",
		"approved_by":       "cto",
		"reason":            "Emergency recovery with explicit CTO approval.",
		"allowed_scope":     "Only the recovery patch named in RENA-123.",
		"checkpoint":        "Review before any feature expansion.",
		"verification_plan": "CTO verifies PRD/design clarity in the override record before merge.",
	})

	err := enforceFromScratchLaunchGate(townRoot, "alpha", "alpha-123", &beadInfo{Title: "Implement API"}, launchGateDispatchOptions{})
	if err != nil {
		t.Fatalf("valid override should allow implementation: %v", err)
	}
}

func TestLaunchGateBlocksOverrideWithoutScopeCheckpointAndVerification(t *testing.T) {
	townRoot := t.TempDir()
	repoRoot := writeLaunchGateMarker(t, townRoot, "alpha")
	writeLaunchGateJSON(t, filepath.Join(repoRoot, launchGateOverrideRelPath), map[string]any{
		"contract":        launchGateOverrideContract,
		"decision":        "allow_implementation_without_mol_idea_to_plan",
		"paperclip_issue": "RENA-123",
		"approved_by":     "cto",
		"reason":          "Emergency recovery with explicit CTO approval.",
	})

	err := enforceFromScratchLaunchGate(townRoot, "alpha", "alpha-123", &beadInfo{Title: "Implement API"}, launchGateDispatchOptions{})
	if err == nil {
		t.Fatal("override without scope/checkpoint/verification should block")
	}
	msg := err.Error()
	for _, want := range []string{"allowed_scope", "expires_at or checkpoint", "verification_plan"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("block error missing %q:\n%s", want, msg)
		}
	}
}

func TestLaunchGateBlocksOverrideWithoutPaperclipOrGastownRecord(t *testing.T) {
	townRoot := t.TempDir()
	repoRoot := writeLaunchGateMarker(t, townRoot, "alpha")
	writeLaunchGateJSON(t, filepath.Join(repoRoot, launchGateOverrideRelPath), map[string]any{
		"contract":          launchGateOverrideContract,
		"decision":          "allow_implementation_without_mol_idea_to_plan",
		"approved_by":       "cto",
		"reason":            "Emergency recovery with explicit CTO approval.",
		"allowed_scope":     "Only the recovery patch.",
		"checkpoint":        "Review before expansion.",
		"verification_plan": "CTO verifies PRD/design clarity before merge.",
	})

	err := enforceFromScratchLaunchGate(townRoot, "alpha", "alpha-123", &beadInfo{Title: "Implement API"}, launchGateDispatchOptions{})
	if err == nil {
		t.Fatal("override without Paperclip or Gastown record should block")
	}
	if !strings.Contains(err.Error(), "paperclip_issue or gastown_bead") {
		t.Fatalf("block error should mention missing durable override record:\n%s", err)
	}
}

func TestLaunchGateAllowsCompleteReceipt(t *testing.T) {
	townRoot := t.TempDir()
	repoRoot := writeLaunchGateMarker(t, townRoot, "alpha")
	writeLaunchGateReceiptArtifacts(t, repoRoot)
	writeLaunchGateJSON(t, filepath.Join(repoRoot, launchGateReceiptRelPath), map[string]any{
		"contract":    launchGateReceiptContract,
		"decision":    "allow",
		"approved_by": "mayor",
		"approved_prd": map[string]any{
			"review_id":           "alpha-plan",
			"draft_path":          ".prd-reviews/alpha-plan/prd-draft.md",
			"review_path":         ".prd-reviews/alpha-plan/prd-review.md",
			"human_clarification": true,
		},
		"design": map[string]any{
			"design_id":  "alpha-design",
			"design_doc": ".designs/alpha-design/design-doc.md",
		},
		"plan_reviews": map[string]any{
			"prd_alignment": []string{
				".plan-reviews/alpha-plan/prd-align-round-1.md",
				".plan-reviews/alpha-plan/prd-align-round-2.md",
				".plan-reviews/alpha-plan/prd-align-round-3.md",
			},
			"plan_review": []string{
				".plan-reviews/alpha-plan/review-round-1.md",
				".plan-reviews/alpha-plan/review-round-2.md",
				".plan-reviews/alpha-plan/review-round-3.md",
			},
		},
		"beads": map[string]any{
			"created_artifact": ".gastown/launch-gate/beads-created.md",
			"epic_id":          "alpha-epic",
			"created_count":    4,
			"verification_passes": []map[string]any{
				{"pass": 1, "artifact": ".gastown/launch-gate/verify-beads-pass-1.md"},
				{"pass": 2, "artifact": ".gastown/launch-gate/verify-beads-pass-2.md"},
				{"pass": 3, "artifact": ".gastown/launch-gate/verify-beads-pass-3.md"},
			},
		},
	})

	err := enforceFromScratchLaunchGate(townRoot, "alpha", "alpha-123", &beadInfo{Title: "Implement API"}, launchGateDispatchOptions{})
	if err != nil {
		t.Fatalf("complete receipt should allow implementation: %v", err)
	}
}

func TestLaunchGateBlocksReceiptWithoutHumanClarificationApproval(t *testing.T) {
	townRoot := t.TempDir()
	repoRoot := writeLaunchGateMarker(t, townRoot, "alpha")
	writeLaunchGateReceiptArtifacts(t, repoRoot)
	writeLaunchGateJSON(t, filepath.Join(repoRoot, launchGateReceiptRelPath), map[string]any{
		"contract": launchGateReceiptContract,
		"decision": "allow",
		"approved_prd": map[string]any{
			"review_id":           "alpha-plan",
			"draft_path":          ".prd-reviews/alpha-plan/prd-draft.md",
			"review_path":         ".prd-reviews/alpha-plan/prd-review.md",
			"human_clarification": false,
		},
		"design": map[string]any{
			"design_id":  "alpha-design",
			"design_doc": ".designs/alpha-design/design-doc.md",
		},
		"plan_reviews": map[string]any{
			"prd_alignment": []string{
				".plan-reviews/alpha-plan/prd-align-round-1.md",
				".plan-reviews/alpha-plan/prd-align-round-2.md",
				".plan-reviews/alpha-plan/prd-align-round-3.md",
			},
			"plan_review": []string{
				".plan-reviews/alpha-plan/review-round-1.md",
				".plan-reviews/alpha-plan/review-round-2.md",
				".plan-reviews/alpha-plan/review-round-3.md",
			},
		},
		"beads": map[string]any{
			"created_artifact": ".gastown/launch-gate/beads-created.md",
			"epic_id":          "alpha-epic",
			"created_count":    4,
			"verification_passes": []map[string]any{
				{"pass": 1, "artifact": ".gastown/launch-gate/verify-beads-pass-1.md"},
				{"pass": 2, "artifact": ".gastown/launch-gate/verify-beads-pass-2.md"},
				{"pass": 3, "artifact": ".gastown/launch-gate/verify-beads-pass-3.md"},
			},
		},
	})

	err := enforceFromScratchLaunchGate(townRoot, "alpha", "alpha-123", &beadInfo{Title: "Implement API"}, launchGateDispatchOptions{})
	if err == nil {
		t.Fatal("receipt without approved_by and human_clarification should block")
	}
	msg := err.Error()
	for _, want := range []string{"approved_by", "human_clarification"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("block error missing %q:\n%s", want, msg)
		}
	}
}

func writeLaunchGateMarker(t *testing.T, townRoot, rigName string) string {
	t.Helper()
	repoRoot := filepath.Join(townRoot, rigName, "refinery", "rig")
	if err := os.MkdirAll(filepath.Join(repoRoot, ".gastown"), 0755); err != nil {
		t.Fatalf("mkdir marker dir: %v", err)
	}
	writeLaunchGateJSON(t, filepath.Join(repoRoot, launchGateMarkerRelPath), map[string]any{
		"contract":   launchGateMarkerContract,
		"project_id": rigName,
		"mode":       "from_scratch",
		"required":   true,
	})
	return repoRoot
}

func writeLaunchGateReceiptArtifacts(t *testing.T, repoRoot string) {
	t.Helper()
	writeLaunchGateFile(t, filepath.Join(repoRoot, ".prd-reviews", "alpha-plan", "prd-draft.md"), "# PRD\n\n## Clarifications from Human Review\nA: yes\n")
	writeLaunchGateFile(t, filepath.Join(repoRoot, ".prd-reviews", "alpha-plan", "prd-review.md"), "# PRD Review\n")
	writeLaunchGateFile(t, filepath.Join(repoRoot, ".designs", "alpha-design", "design-doc.md"), "# Design\n")
	for _, name := range []string{
		"prd-align-round-1.md",
		"prd-align-round-2.md",
		"prd-align-round-3.md",
		"review-round-1.md",
		"review-round-2.md",
		"review-round-3.md",
	} {
		writeLaunchGateFile(t, filepath.Join(repoRoot, ".plan-reviews", "alpha-plan", name), "# "+name+"\n")
	}
	for _, name := range []string{
		"beads-created.md",
		"verify-beads-pass-1.md",
		"verify-beads-pass-2.md",
		"verify-beads-pass-3.md",
	} {
		writeLaunchGateFile(t, filepath.Join(repoRoot, ".gastown", "launch-gate", name), "# "+name+"\n")
	}
}

func writeLaunchGateJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	writeLaunchGateFile(t, path, string(data)+"\n")
}

func writeLaunchGateFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
