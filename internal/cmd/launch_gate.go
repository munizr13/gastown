package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/workspace"
)

const (
	launchGateMarkerRelPath   = ".gastown/launch-gate.json"
	launchGateReceiptRelPath  = ".gastown/launch-gate/receipt.json"
	launchGateOverrideRelPath = ".gastown/launch-gate/override.json"
	launchGateBeadsCreatedRel = ".gastown/launch-gate/beads-created.md"

	launchGateMarkerContract   = "gastown.from_scratch_launch_gate.v1"
	launchGateReceiptContract  = "gastown.from_scratch_launch_gate.receipt.v1"
	launchGateOverrideContract = "gastown.from_scratch_launch_gate.override.v1"
)

type launchGateMarker struct {
	Contract  string `json:"contract"`
	ProjectID string `json:"project_id,omitempty"`
	Mode      string `json:"mode,omitempty"`
	Required  *bool  `json:"required,omitempty"`
	ReviewID  string `json:"review_id,omitempty"`
	DesignID  string `json:"design_id,omitempty"`
	Notes     string `json:"notes,omitempty"`
}

type launchGateReceipt struct {
	Contract    string `json:"contract"`
	Decision    string `json:"decision"`
	ApprovedBy  string `json:"approved_by,omitempty"`
	PaperclipID string `json:"paperclip_issue,omitempty"`
	GastownBead string `json:"gastown_bead,omitempty"`
	ApprovedPRD struct {
		ReviewID           string `json:"review_id,omitempty"`
		DraftPath          string `json:"draft_path,omitempty"`
		ReviewPath         string `json:"review_path,omitempty"`
		HumanClarification bool   `json:"human_clarification"`
	} `json:"approved_prd"`
	Design struct {
		DesignID  string `json:"design_id,omitempty"`
		DesignDoc string `json:"design_doc,omitempty"`
	} `json:"design"`
	PlanReviews struct {
		PRDAlignment []string `json:"prd_alignment,omitempty"`
		PlanReview   []string `json:"plan_review,omitempty"`
	} `json:"plan_reviews"`
	Beads struct {
		CreatedArtifact    string                       `json:"created_artifact,omitempty"`
		EpicID             string                       `json:"epic_id,omitempty"`
		CreatedCount       int                          `json:"created_count,omitempty"`
		VerificationPasses []launchGateVerificationPass `json:"verification_passes,omitempty"`
	} `json:"beads"`
}

type launchGateVerificationPass struct {
	Pass     int    `json:"pass"`
	Artifact string `json:"artifact"`
}

type launchGateOverride struct {
	Contract    string `json:"contract"`
	Decision    string `json:"decision"`
	PaperclipID string `json:"paperclip_issue,omitempty"`
	GastownBead string `json:"gastown_bead,omitempty"`
	ApprovedBy  string `json:"approved_by,omitempty"`
	Reason      string `json:"reason,omitempty"`
	ExpiresAt   string `json:"expires_at,omitempty"`
}

type launchGateDispatchOptions struct {
	FormulaName string
	ReviewOnly  bool
	Args        string
	Vars        []string
	Mode        string
	Agent       string
	Target      string
}

type launchGateEvaluation struct {
	RigName      string
	RepoRoot     string
	MarkerPath   string
	ReceiptPath  string
	OverridePath string
	Status       string
	Reason       string
	Missing      []string
}

var (
	launchGateCheckBead string
)

var launchGateCmd = &cobra.Command{
	Use:     "launch-gate",
	GroupID: GroupDiag,
	Short:   "Inspect from-scratch project launch gates",
}

var launchGateCheckCmd = &cobra.Command{
	Use:   "check <rig>",
	Short: "Check whether a rig may dispatch implementation work",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		townRoot, err := workspace.FindFromCwdOrError()
		if err != nil {
			return err
		}
		var info *beadInfo
		if launchGateCheckBead != "" {
			info, err = getBeadInfo(launchGateCheckBead)
			if err != nil {
				return err
			}
		}
		eval, err := evaluateFromScratchLaunchGate(townRoot, args[0], launchGateCheckBead, info, launchGateDispatchOptions{})
		if err != nil {
			return err
		}
		printLaunchGateEvaluation(eval)
		if eval.Status == "blocked" {
			return fmt.Errorf("launch gate blocked for rig %q", args[0])
		}
		return nil
	},
}

func init() {
	launchGateCheckCmd.Flags().StringVar(&launchGateCheckBead, "bead", "", "Optional bead ID to classify planning vs implementation dispatch")
	launchGateCmd.AddCommand(launchGateCheckCmd)
	rootCmd.AddCommand(launchGateCmd)
}

func enforceFromScratchLaunchGate(townRoot, rigName, beadID string, info *beadInfo, opts launchGateDispatchOptions) error {
	eval, err := evaluateFromScratchLaunchGate(townRoot, rigName, beadID, info, opts)
	if err != nil {
		return err
	}
	if eval.Status != "blocked" {
		return nil
	}

	lines := []string{
		fmt.Sprintf("from-scratch project launch gate blocked implementation dispatch for %s in rig %s", beadID, rigName),
		fmt.Sprintf("marker: %s", eval.MarkerPath),
		"",
		"Required before implementation dispatch:",
		"- approved PRD with human clarification answers",
		"- design document from the design convoy",
		"- PRD-alignment review logs",
		"- plan self-review logs",
		"- bead creation evidence",
		"- three bead verification pass artifacts",
		"",
		"Missing or invalid evidence:",
	}
	for _, missing := range eval.Missing {
		lines = append(lines, "- "+missing)
	}
	lines = append(lines, "",
		fmt.Sprintf("To proceed normally: finish mol-idea-to-plan and write %s", launchGateReceiptRelPath),
		fmt.Sprintf("To override: write %s with a Paperclip issue or Gastown bead reference", launchGateOverrideRelPath),
	)
	return fmt.Errorf("%s", strings.Join(lines, "\n"))
}

func enforceFromScratchLaunchGateForTarget(townRoot, target, beadID string, info *beadInfo, opts launchGateDispatchOptions) error {
	rigName, ok := launchGateRigNameFromTarget(target)
	if !ok {
		return nil
	}
	opts.Target = target
	return enforceFromScratchLaunchGate(townRoot, rigName, beadID, info, opts)
}

func evaluateFromScratchLaunchGate(townRoot, rigName, beadID string, info *beadInfo, opts launchGateDispatchOptions) (*launchGateEvaluation, error) {
	repoRoot, markerPath, found := findLaunchGateMarker(townRoot, rigName)
	eval := &launchGateEvaluation{
		RigName:    rigName,
		RepoRoot:   repoRoot,
		MarkerPath: markerPath,
		Status:     "not_required",
		Reason:     "no launch-gate marker found",
	}
	if !found {
		return eval, nil
	}
	eval.ReceiptPath = filepath.Join(repoRoot, launchGateReceiptRelPath)
	eval.OverridePath = filepath.Join(repoRoot, launchGateOverrideRelPath)

	var marker launchGateMarker
	if err := readJSONFile(markerPath, &marker); err != nil {
		eval.Status = "blocked"
		eval.Reason = "invalid marker"
		eval.Missing = []string{fmt.Sprintf("valid marker JSON at %s: %v", launchGateMarkerRelPath, err)}
		return eval, nil
	}
	if marker.Contract != launchGateMarkerContract {
		eval.Status = "blocked"
		eval.Reason = "invalid marker contract"
		eval.Missing = []string{fmt.Sprintf("%s contract must be %q", launchGateMarkerRelPath, launchGateMarkerContract)}
		return eval, nil
	}
	if marker.Required != nil && !*marker.Required {
		eval.Status = "disabled"
		eval.Reason = "marker explicitly disabled"
		return eval, nil
	}
	if strings.TrimSpace(marker.Mode) != "" && !strings.EqualFold(marker.Mode, "from_scratch") {
		eval.Status = "disabled"
		eval.Reason = "marker is not in from_scratch mode"
		return eval, nil
	}

	if isLaunchPlanningDispatch(info, opts) {
		eval.Status = "planning_allowed"
		eval.Reason = "planning/review dispatch is allowed before implementation gate completion"
		return eval, nil
	}

	if ok, missing := validateLaunchGateOverride(eval.OverridePath); ok {
		eval.Status = "override_allowed"
		eval.Reason = "explicit override recorded"
		return eval, nil
	} else if len(missing) > 0 && fileExists(eval.OverridePath) {
		eval.Missing = append(eval.Missing, missing...)
	}

	if ok, missing := validateLaunchGateReceipt(repoRoot, marker, eval.ReceiptPath); ok {
		eval.Status = "ready"
		eval.Reason = "mol-idea-to-plan completion receipt is valid"
		return eval, nil
	} else {
		eval.Missing = append(eval.Missing, missing...)
	}

	eval.Status = "blocked"
	eval.Reason = "completion receipt or explicit override required"
	eval.Missing = launchGateUniqueStrings(eval.Missing)
	sort.Strings(eval.Missing)
	return eval, nil
}

func printLaunchGateEvaluation(eval *launchGateEvaluation) {
	fmt.Printf("Launch gate: %s\n", eval.Status)
	if eval.Reason != "" {
		fmt.Printf("Reason: %s\n", eval.Reason)
	}
	if eval.RepoRoot != "" {
		fmt.Printf("Repo: %s\n", eval.RepoRoot)
	}
	if eval.MarkerPath != "" {
		fmt.Printf("Marker: %s\n", eval.MarkerPath)
	}
	if eval.ReceiptPath != "" {
		fmt.Printf("Receipt: %s\n", eval.ReceiptPath)
	}
	if eval.OverridePath != "" {
		fmt.Printf("Override: %s\n", eval.OverridePath)
	}
	if len(eval.Missing) > 0 {
		fmt.Println("Missing:")
		for _, missing := range eval.Missing {
			fmt.Printf("- %s\n", missing)
		}
	}
}

func findLaunchGateMarker(townRoot, rigName string) (repoRoot, markerPath string, found bool) {
	for _, root := range launchGateRigRepoCandidates(townRoot, rigName) {
		path := filepath.Join(root, launchGateMarkerRelPath)
		if fileExists(path) {
			return root, path, true
		}
		if repoRoot == "" && dirExists(root) {
			repoRoot = root
			markerPath = path
		}
	}
	return repoRoot, markerPath, false
}

func launchGateRigRepoCandidates(townRoot, rigName string) []string {
	return []string{
		filepath.Join(townRoot, rigName, "refinery", "rig"),
		filepath.Join(townRoot, rigName, "mayor", "rig"),
		filepath.Join(townRoot, "mayor", rigName, "rig"),
		filepath.Join(townRoot, rigName, "rig"),
	}
}

func launchGateRigNameFromTarget(target string) (string, bool) {
	target = strings.Trim(target, "/")
	if target == "" || target == "." {
		return "", false
	}
	if rigName, isRig := IsRigName(target); isRig {
		return rigName, true
	}
	parts := strings.Split(target, "/")
	if len(parts) < 2 {
		return "", false
	}
	if _, isRig := IsRigName(parts[0]); !isRig {
		return "", false
	}
	switch parts[1] {
	case "polecats", "crew":
		return parts[0], true
	default:
		return "", false
	}
}

func isLaunchPlanningDispatch(info *beadInfo, opts launchGateDispatchOptions) bool {
	switch strings.TrimSpace(opts.FormulaName) {
	case "mol-idea-to-plan", "mol-prd-review", "design":
		return true
	}
	if opts.ReviewOnly {
		return true
	}
	textParts := []string{opts.Args, strings.Join(opts.Vars, "\n"), opts.Mode, opts.Agent, opts.Target}
	if info != nil {
		textParts = append(textParts, info.Title, info.Description, strings.Join(info.Labels, " "))
	}
	text := strings.ToLower(strings.Join(textParts, "\n"))
	for _, needle := range []string{
		".prd-reviews/",
		".designs/",
		".plan-reviews/",
		"prd review",
		"prd alignment",
		"plan self-review",
		"review an implementation plan",
		"verify beads coverage",
		"final verification pass",
		"convert approved plan to beads",
		"structure idea into draft prd",
		"gather human clarifications",
		"design convoy",
		"mail your full report back when done",
	} {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

func validateLaunchGateOverride(path string) (bool, []string) {
	if !fileExists(path) {
		return false, []string{fmt.Sprintf("completion receipt %s is absent", launchGateReceiptRelPath)}
	}
	var override launchGateOverride
	if err := readJSONFile(path, &override); err != nil {
		return false, []string{fmt.Sprintf("valid override JSON at %s: %v", launchGateOverrideRelPath, err)}
	}
	missing := []string{}
	if override.Contract != launchGateOverrideContract {
		missing = append(missing, fmt.Sprintf("%s contract must be %q", launchGateOverrideRelPath, launchGateOverrideContract))
	}
	if !strings.Contains(strings.ToLower(override.Decision), "allow") && !strings.Contains(strings.ToLower(override.Decision), "override") {
		missing = append(missing, fmt.Sprintf("%s decision must explicitly allow override", launchGateOverrideRelPath))
	}
	if strings.TrimSpace(override.PaperclipID) == "" && strings.TrimSpace(override.GastownBead) == "" {
		missing = append(missing, fmt.Sprintf("%s must reference paperclip_issue or gastown_bead", launchGateOverrideRelPath))
	}
	if strings.TrimSpace(override.ApprovedBy) == "" {
		missing = append(missing, fmt.Sprintf("%s must include approved_by", launchGateOverrideRelPath))
	}
	if strings.TrimSpace(override.Reason) == "" {
		missing = append(missing, fmt.Sprintf("%s must include reason", launchGateOverrideRelPath))
	}
	if override.ExpiresAt != "" {
		expires, err := time.Parse(time.RFC3339, override.ExpiresAt)
		if err != nil {
			missing = append(missing, fmt.Sprintf("%s expires_at must be RFC3339", launchGateOverrideRelPath))
		} else if time.Now().After(expires) {
			missing = append(missing, fmt.Sprintf("%s has expired", launchGateOverrideRelPath))
		}
	}
	return len(missing) == 0, missing
}

func validateLaunchGateReceipt(repoRoot string, marker launchGateMarker, path string) (bool, []string) {
	if !fileExists(path) {
		return false, []string{fmt.Sprintf("completion receipt %s is absent", launchGateReceiptRelPath)}
	}
	var receipt launchGateReceipt
	if err := readJSONFile(path, &receipt); err != nil {
		return false, []string{fmt.Sprintf("valid receipt JSON at %s: %v", launchGateReceiptRelPath, err)}
	}

	missing := []string{}
	if receipt.Contract != launchGateReceiptContract {
		missing = append(missing, fmt.Sprintf("%s contract must be %q", launchGateReceiptRelPath, launchGateReceiptContract))
	}
	if !isAllowDecision(receipt.Decision) {
		missing = append(missing, fmt.Sprintf("%s decision must be allow/approved", launchGateReceiptRelPath))
	}
	if strings.TrimSpace(receipt.ApprovedBy) == "" {
		missing = append(missing, fmt.Sprintf("%s must include approved_by", launchGateReceiptRelPath))
	}
	if !receipt.ApprovedPRD.HumanClarification {
		missing = append(missing, "approved_prd.human_clarification must be true")
	}

	reviewID := firstNonEmpty(receipt.ApprovedPRD.ReviewID, marker.ReviewID)
	designID := firstNonEmpty(receipt.Design.DesignID, marker.DesignID)
	prdDraft := firstNonEmpty(receipt.ApprovedPRD.DraftPath, defaultReviewArtifact(reviewID, "prd-draft.md"))
	prdReview := firstNonEmpty(receipt.ApprovedPRD.ReviewPath, defaultReviewArtifact(reviewID, "prd-review.md"))
	designDoc := firstNonEmpty(receipt.Design.DesignDoc, defaultDesignArtifact(designID))

	missing = append(missing, requireRelativeFile(repoRoot, "approved PRD draft", prdDraft)...)
	missing = append(missing, requireRelativeFile(repoRoot, "PRD review synthesis", prdReview)...)
	missing = append(missing, requireRelativeFile(repoRoot, "design document", designDoc)...)
	if prdDraft != "" && fileExists(filepath.Join(repoRoot, prdDraft)) {
		data, err := os.ReadFile(filepath.Join(repoRoot, prdDraft))
		if err != nil {
			missing = append(missing, fmt.Sprintf("read approved PRD draft %s: %v", prdDraft, err))
		} else if !strings.Contains(string(data), "Clarifications from Human Review") {
			missing = append(missing, fmt.Sprintf("approved PRD draft %s must include Clarifications from Human Review", prdDraft))
		}
	}

	prdAlignment := receipt.PlanReviews.PRDAlignment
	if len(prdAlignment) == 0 && reviewID != "" {
		prdAlignment = []string{
			filepath.ToSlash(filepath.Join(".plan-reviews", reviewID, "prd-align-round-1.md")),
			filepath.ToSlash(filepath.Join(".plan-reviews", reviewID, "prd-align-round-2.md")),
			filepath.ToSlash(filepath.Join(".plan-reviews", reviewID, "prd-align-round-3.md")),
		}
	}
	planReview := receipt.PlanReviews.PlanReview
	if len(planReview) == 0 && reviewID != "" {
		planReview = []string{
			filepath.ToSlash(filepath.Join(".plan-reviews", reviewID, "review-round-1.md")),
			filepath.ToSlash(filepath.Join(".plan-reviews", reviewID, "review-round-2.md")),
			filepath.ToSlash(filepath.Join(".plan-reviews", reviewID, "review-round-3.md")),
		}
	}
	if len(prdAlignment) < 3 {
		missing = append(missing, "three PRD-alignment review artifacts")
	}
	for _, artifact := range prdAlignment {
		missing = append(missing, requireRelativeFile(repoRoot, "PRD-alignment review artifact", artifact)...)
	}
	if len(planReview) < 3 {
		missing = append(missing, "three plan self-review artifacts")
	}
	for _, artifact := range planReview {
		missing = append(missing, requireRelativeFile(repoRoot, "plan self-review artifact", artifact)...)
	}

	if strings.TrimSpace(receipt.Beads.EpicID) == "" {
		missing = append(missing, "bead creation epic_id")
	}
	if receipt.Beads.CreatedCount <= 0 {
		missing = append(missing, "bead creation created_count > 0")
	}
	beadsCreatedArtifact := firstNonEmpty(receipt.Beads.CreatedArtifact, launchGateBeadsCreatedRel)
	missing = append(missing, requireRelativeFile(repoRoot, "bead creation artifact", beadsCreatedArtifact)...)
	passArtifacts := map[int]string{}
	for _, pass := range receipt.Beads.VerificationPasses {
		if pass.Pass > 0 {
			passArtifacts[pass.Pass] = pass.Artifact
		}
	}
	for pass := 1; pass <= 3; pass++ {
		artifact, ok := passArtifacts[pass]
		if !ok {
			missing = append(missing, fmt.Sprintf("bead verification pass %d artifact", pass))
			continue
		}
		missing = append(missing, requireRelativeFile(repoRoot, fmt.Sprintf("bead verification pass %d artifact", pass), artifact)...)
	}

	missing = launchGateUniqueStrings(missing)
	return len(missing) == 0, missing
}

func defaultReviewArtifact(reviewID, name string) string {
	if strings.TrimSpace(reviewID) == "" {
		return ""
	}
	return filepath.ToSlash(filepath.Join(".prd-reviews", reviewID, name))
}

func defaultDesignArtifact(designID string) string {
	if strings.TrimSpace(designID) == "" {
		return ""
	}
	return filepath.ToSlash(filepath.Join(".designs", designID, "design-doc.md"))
}

func requireRelativeFile(root, label, relPath string) []string {
	if strings.TrimSpace(relPath) == "" {
		return []string{label + " path"}
	}
	if filepath.IsAbs(relPath) {
		return []string{fmt.Sprintf("%s must be repo-relative, got %s", label, relPath)}
	}
	if !fileExists(filepath.Join(root, relPath)) {
		return []string{fmt.Sprintf("%s missing at %s", label, relPath)}
	}
	return nil
}

func readJSONFile(path string, out any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func isAllowDecision(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return value == "allow" || value == "approved" || value == "ready"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func launchGateUniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
