package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/deacon"
	"github.com/steveyegge/gastown/internal/formula"
)

const (
	// bdMolTimeout is the timeout for bd molecule operations.
	bdMolTimeout = 15 * time.Second

	// dogCloseMaxAttempts / dogCloseRetryDelay bound the retry on `bd close` for
	// dog wisps. A transient Dolt slowdown (the connection-churn window) can make
	// a single close fail, and without a retry the wisp stays OPEN forever — a
	// root cause of the dog wisp flood (gt-ye21). Retrying turns a transient
	// failure back into a clean close instead of a permanent orphan.
	dogCloseMaxAttempts = 3
	dogCloseRetryDelay  = 500 * time.Millisecond
)

// closeWisp runs `bd close <id>` (plus any extra args) with bounded retries so a
// transient Dolt error does not leave the wisp open. Returns the final error if
// every attempt fails.
func (dm *dogMol) closeWisp(id string, extra ...string) error {
	args := append([]string{"close", id}, extra...)
	var err error
	for attempt := 1; attempt <= dogCloseMaxAttempts; attempt++ {
		if _, err = dm.runBd(args...); err == nil {
			return nil
		}
		if errors.Is(err, deacon.ErrPatrolHeld) {
			return err
		}
		if attempt < dogCloseMaxAttempts {
			time.Sleep(time.Duration(attempt) * dogCloseRetryDelay)
		}
	}
	return err
}

// dogMol tracks a molecule (wisp) lifecycle for a daemon dog patrol.
// Graceful degradation: if bd fails, the dog still does its work — molecule
// tracking is observability, not control flow.
type dogMol struct {
	formulaName string
	rootID      string            // Root wisp ID (e.g., "gt-wisp-abc123"), empty if pour failed.
	stepIDs     map[string]string // step slug -> wisp issue ID
	bdPath      string
	townRoot    string
	logger      interface{ Printf(string, ...interface{}) }
}

// pourDogMolecule creates an ephemeral wisp molecule from a formula.
// Returns a dogMol handle for closing steps. If bd fails, returns a no-op
// handle so the caller can proceed without error checking.
func (d *Daemon) pourDogMolecule(formulaName string, vars map[string]string) *dogMol {
	dm := &dogMol{
		formulaName: formulaName,
		stepIDs:     make(map[string]string),
		bdPath:      d.bdPath,
		townRoot:    d.config.TownRoot,
		logger:      d.logger,
	}

	// Build args: bd mol wisp <formula> --var k=v ...
	args := []string{"mol", "wisp", formulaName}
	for k, v := range vars {
		args = append(args, "--var", fmt.Sprintf("%s=%s", k, v))
	}

	out, err := dm.runBd(args...)
	if err != nil {
		d.logger.Printf("dog_molecule: pour %s failed (non-fatal): %v", formulaName, err)
		return dm
	}

	// Parse root ID from output. bd mol wisp prints the root ID on the first line.
	// Example output: "✓ Spawned wisp: gt-wisp-abc123 — Reap stale wisps..."
	dm.rootID = parseWispID(out)
	if dm.rootID == "" {
		d.logger.Printf("dog_molecule: pour %s: could not parse root ID from output: %s", formulaName, out)
		return dm
	}

	// Discover step IDs by listing children of the root wisp.
	dm.discoverSteps()

	d.logger.Printf("dog_molecule: poured %s → %s (%d steps)", formulaName, dm.rootID, len(dm.stepIDs))
	return dm
}

// closeStep marks a molecule step as closed.
func (dm *dogMol) closeStep(stepSlug string, receipt ...string) {
	if dm.rootID == "" {
		return // No molecule — graceful degradation.
	}

	stepID, ok := dm.stepIDs[stepSlug]
	if !ok {
		dm.logger.Printf("dog_molecule: closeStep %q: unknown step (known: %v)", stepSlug, dm.knownSteps())
		return
	}

	reason := "completed: daemon reported step finished"
	if len(receipt) > 0 {
		reason = strings.Join(receipt, "; ")
	}
	if err := dm.closeWisp(stepID, "--reason", reason); err != nil {
		dm.logger.Printf("dog_molecule: close step %s (%s) failed after %d attempts (non-fatal): %v", stepSlug, stepID, dogCloseMaxAttempts, err)
		return
	}
}

// failStep marks a molecule step as failed with a reason.
func (dm *dogMol) failStep(stepSlug, reason string) {
	if dm.rootID == "" {
		return
	}

	stepID, ok := dm.stepIDs[stepSlug]
	if !ok {
		// Keep the reason visible even when the slug cannot be resolved
		// (pour sometimes materializes fewer steps than the formula defines).
		dm.logger.Printf("dog_molecule: failStep %q: unknown step (known: %v, reason: %s)", stepSlug, dm.knownSteps(), reason)
		return
	}

	if err := dm.closeWisp(stepID, "--reason", "failed: "+reason); err != nil {
		dm.logger.Printf("dog_molecule: fail step %s (%s) failed after %d attempts (non-fatal): %v", stepSlug, stepID, dogCloseMaxAttempts, err)
	}
}

// close retires the daemon tracking envelope. Unreported steps are explicitly
// cancelled, never reported as executed. A root closure is lifecycle cleanup;
// operation evidence belongs to explicit step receipts, including failures.
func (dm *dogMol) close() {
	if dm.rootID == "" {
		return
	}

	// Close any step wisps that were never explicitly closed/failed.
	if err := dm.closeRemainingSteps(); err != nil {
		dm.logger.Printf("dog_molecule: retain root %s: child retirement unconfirmed: %v", dm.rootID, err)
		return
	}

	if err := dm.closeWisp(dm.rootID, "--reason", "retired: daemon tracking ended; root closure does not prove step execution"); err != nil {
		dm.logger.Printf("dog_molecule: close root %s failed after %d attempts (non-fatal): %v", dm.rootID, dogCloseMaxAttempts, err)
	}
}

// closeRemainingSteps queries all children of the root wisp and closes any that
// are still open, recording that no execution receipt was supplied. This avoids
// orphan buildup without manufacturing successful operation records.
func (dm *dogMol) closeRemainingSteps() error {
	if dm.rootID == "" {
		return nil
	}
	if err := deacon.CheckPatrolAllowed(dm.townRoot); err != nil {
		return err
	}

	out, err := dm.runBd("show", dm.rootID, "--children", "--json")
	if err != nil {
		dm.logger.Printf("dog_molecule: closeRemainingSteps: list children of %s failed: %v", dm.rootID, err)
		return err
	}

	children, parseErr := parseChildrenJSON(out, dm.rootID)
	if parseErr != nil {
		dm.logger.Printf("dog_molecule: closeRemainingSteps: parse children JSON for %s failed: %v", dm.rootID, parseErr)
		return parseErr
	}

	var remaining []childInfo
	seenChildren := make(map[string]bool)
	for _, child := range children {
		if child.ID == "" || child.Status == "" {
			return fmt.Errorf("child identity or status is missing")
		}
		if seenChildren[child.ID] {
			return fmt.Errorf("duplicate child identity %s", child.ID)
		}
		seenChildren[child.ID] = true
		switch child.Status {
		case "open", "hooked", "in_progress", "blocked", "deferred":
			remaining = append(remaining, child)
		case "closed":
			// Already has a terminal receipt; leave it intact.
		default:
			return fmt.Errorf("child %s has unknown status %q", child.ID, child.Status)
		}
	}

	// Multi-pass close: a step that depends on another open step fails with
	// "blocked by open issues" — but succeeds once its blocker is closed in a
	// later pass. Iterating in arbitrary order in a single pass left dependent
	// steps open forever (~13 orphan wisps/hour accumulated town-wide). Passes
	// are bounded by len(remaining): each productive pass closes >= 1 wisp.
	closed := 0
	for pass := 0; pass < len(children)+1 && len(remaining) > 0; pass++ {
		var next []childInfo
		for _, child := range remaining {
			if err := dm.closeWisp(child.ID, "--reason", "cancelled: no execution receipt; daemon tracking ended"); err != nil {
				next = append(next, child)
			} else {
				closed++
			}
		}
		if len(next) == len(remaining) {
			remaining = next
			break // no progress — plain close cannot resolve what is left
		}
		remaining = next
	}

	// Force fallback for whatever plain close could not resolve (cyclic step
	// dependencies have been observed). These are this molecule's own step
	// wisps; force only cancels their tracking and never asserts execution.
	var failed []string
	for _, child := range remaining {
		if err := dm.closeWisp(child.ID, "--force", "--reason", "cancelled: no execution receipt; daemon tracking ended"); err != nil {
			dm.logger.Printf("dog_molecule: closeRemainingSteps: force-close %s failed after %d attempts: %v", child.ID, dogCloseMaxAttempts, err)
			failed = append(failed, child.ID)
		} else {
			closed++
		}
	}

	if closed > 0 {
		dm.logger.Printf("dog_molecule: closeRemainingSteps: cancelled %d unreported step wisp(s) under %s", closed, dm.rootID)
	}
	if len(failed) > 0 {
		return fmt.Errorf("could not retire child wisps: %s", strings.Join(failed, ", "))
	}
	return nil
}

// discoverSteps lists children of the root wisp and maps step slugs to IDs.
// Exact formula titles bind execution receipts to the intended steps.
func (dm *dogMol) discoverSteps() {
	if dm.rootID == "" {
		return
	}
	dm.stepIDs = make(map[string]string)

	// Read only this molecule's children.
	out, err := dm.runBd("show", dm.rootID, "--children", "--json")
	if err != nil {
		dm.logger.Printf("dog_molecule: discover steps for %s failed: %v", dm.rootID, err)
		return
	}

	children, parseErr := parseChildrenJSON(out, dm.rootID)
	if parseErr != nil {
		dm.logger.Printf("dog_molecule: discover steps: parse children JSON for %s failed: %v", dm.rootID, parseErr)
		return
	}

	// The daemon executes the embedded formula contract. Match its exact step
	// titles instead of guessing from keywords: "Verify export counts" is not
	// the export step, and both backup steps contain "sync" and "backup".
	content, err := formula.GetEmbeddedFormulaContent(dm.formulaName)
	if err != nil {
		dm.logger.Printf("dog_molecule: cannot identify formula steps: %v", err)
		return
	}
	definition, err := formula.Parse(content)
	if err != nil {
		dm.logger.Printf("dog_molecule: cannot parse formula steps: %v", err)
		return
	}
	titleCounts := make(map[string]int)
	byTitle := make(map[string][]childInfo)
	idCounts := make(map[string]int)
	for _, step := range definition.Steps {
		titleCounts[step.Title]++
	}
	for _, child := range children {
		byTitle[child.Title] = append(byTitle[child.Title], child)
		idCounts[child.ID]++
	}
	for _, step := range definition.Steps {
		matches := byTitle[step.Title]
		if titleCounts[step.Title] != 1 || len(matches) != 1 || matches[0].ID == "" || idCounts[matches[0].ID] != 1 {
			dm.logger.Printf("dog_molecule: step %s has missing or ambiguous identity; no execution receipt will be assigned", step.ID)
			continue
		}
		dm.stepIDs[step.ID] = matches[0].ID
	}
}

// childInfo holds fields from child wisp JSON used by discoverSteps and
// closeRemainingSteps.
type childInfo struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

// parseChildrenJSON parses the output of `bd show <id> --children --json`.
// bd returns a map keyed by parent ID plus envelope metadata:
// {"hq-wisp-abc": [{...}, ...], "schema_version": 1}.
// For legacy compatibility, a bare array is also accepted.
func parseChildrenJSON(raw string, expectedRoot ...string) ([]childInfo, error) {
	data := bytes.TrimSpace([]byte(raw))
	if len(data) == 0 {
		return nil, fmt.Errorf("empty children JSON")
	}

	var arr []childInfo
	if data[0] == '[' {
		if err := json.Unmarshal(data, &arr); err != nil {
			return nil, err
		}
		return arr, nil
	}

	if data[0] != '{' {
		return nil, fmt.Errorf("unrecognized JSON shape: %.200s", raw)
	}

	var wrapped map[string]json.RawMessage
	if err := json.Unmarshal(data, &wrapped); err != nil {
		return nil, err
	}

	keys := make([]string, 0, len(wrapped))
	for key := range wrapped {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var children []childInfo
	sawChildArray := false
	for _, key := range keys {
		if key == "schema_version" {
			continue
		}
		if len(expectedRoot) > 0 && key != expectedRoot[0] {
			return nil, fmt.Errorf("children belong to root %q, expected %q", key, expectedRoot[0])
		}

		value := bytes.TrimSpace(wrapped[key])
		if len(value) == 0 {
			return nil, fmt.Errorf("empty child payload for key %q", key)
		}
		if value[0] != '[' {
			return nil, fmt.Errorf("non-array child payload for key %q", key)
		}

		var group []childInfo
		if err := json.Unmarshal(value, &group); err != nil {
			return nil, fmt.Errorf("parse child array for key %q: %w", key, err)
		}
		children = append(children, group...)
		sawChildArray = true
	}

	if !sawChildArray {
		return nil, fmt.Errorf("children JSON object has no child arrays")
	}

	return children, nil
}

// knownSteps returns the list of known step slugs for debugging.
func (dm *dogMol) knownSteps() []string {
	var steps []string
	for k := range dm.stepIDs {
		steps = append(steps, k)
	}
	return steps
}

// runBd executes a bd command and returns stdout.
func (dm *dogMol) runBd(args ...string) (string, error) {
	if len(args) > 0 && args[0] != "show" {
		if err := deacon.CheckPatrolAllowed(dm.townRoot); err != nil {
			return "", err
		}
	}

	bdPath := dm.bdPath
	if bdPath == "" {
		bdPath = "bd"
	}

	ctx, cancel := context.WithTimeout(context.Background(), bdMolTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, bdPath, args...)
	beads.ConfigureCommand(cmd, dm.townRoot, filepath.Join(dm.townRoot, ".beads"), beads.SubprocessModeForArgs(args))

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg != "" {
			return "", fmt.Errorf("%s: %s", err, errMsg)
		}
		return "", err
	}

	return strings.TrimSpace(stdout.String()), nil
}

// parseWispID extracts a wisp ID from bd mol wisp output.
// Looks for patterns like "gt-wisp-abc123" or any ID containing "-wisp-".
func parseWispID(output string) string {
	for _, word := range strings.Fields(output) {
		// Strip ANSI codes and punctuation.
		cleaned := stripANSI(word)
		cleaned = strings.TrimRight(cleaned, ".,;:!?")
		if strings.Contains(cleaned, "-wisp-") {
			return cleaned
		}
	}
	// Fallback: look for any bead-like ID (prefix-xxxx pattern).
	for _, word := range strings.Fields(output) {
		cleaned := stripANSI(word)
		cleaned = strings.TrimRight(cleaned, ".,;:!?")
		if len(cleaned) > 3 && strings.Contains(cleaned, "-") && !strings.HasPrefix(cleaned, "--") {
			// Could be a bead ID like "gt-abc123".
			return cleaned
		}
	}
	return ""
}

// stripANSI removes ANSI escape codes from a string.
func stripANSI(s string) string {
	var result strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '\033' {
			// Skip escape sequence.
			i++
			if i < len(s) && s[i] == '[' {
				i++
				for i < len(s) && !((s[i] >= 'A' && s[i] <= 'Z') || (s[i] >= 'a' && s[i] <= 'z')) {
					i++
				}
				if i < len(s) {
					i++ // Skip the terminating letter.
				}
			}
		} else {
			result.WriteByte(s[i])
			i++
		}
	}
	return result.String()
}
