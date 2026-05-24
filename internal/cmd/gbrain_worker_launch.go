package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	envGBrainWorkerLaunchDecisionEnabled    = "GT_GBRAIN_WORKER_LAUNCH_DECISION"
	envGBrainWorkerLaunchDecisionURL        = "GT_GBRAIN_WORKER_LAUNCH_DECISION_URL"
	envGBrainWorkerLaunchDecisionAPIBaseURL = "GT_GBRAIN_WORKER_LAUNCH_DECISION_API_BASE_URL"
	envGBrainWorkerLaunchDecisionLane       = "GT_GBRAIN_WORKER_LAUNCH_DECISION_LANE"
	envGBrainWorkerLaunchDecisionTimeoutMS  = "GT_GBRAIN_WORKER_LAUNCH_DECISION_TIMEOUT_MS"
	envGBrainWorkerLaunchDecisionActionCode = "GT_GBRAIN_WORKER_LAUNCH_ACTION_CODE"
)

const (
	defaultGBrainWorkerLaunchDecisionBaseURL   = "http://127.0.0.1:8000"
	defaultGBrainWorkerLaunchDecisionLane      = "assisted_autonomy"
	defaultGBrainWorkerLaunchDecisionTimeoutMS = 5000
)

type gbrainWorkerLaunchDecisionRequest struct {
	TargetID     string
	ActionCode   string
	WorkerSystem string
	WorkerRole   string
	ActionText   string
}

type gbrainWorkerLaunchDecisionResponse struct {
	Contract string `json:"contract"`
	Request  struct {
		TargetID     string `json:"target_id"`
		Lane         string `json:"lane"`
		ActionCode   string `json:"action_code"`
		WorkerSystem string `json:"worker_system"`
		WorkerRole   string `json:"worker_role"`
	} `json:"request"`
	Decision struct {
		Allowed                  *bool    `json:"allowed"`
		State                    string   `json:"state"`
		Recommendation           string   `json:"recommendation"`
		PolicyMode               string   `json:"policy_mode"`
		BlockingReasonCodes      []string `json:"blocking_reason_codes"`
		WarningReasonCodes       []string `json:"warning_reason_codes"`
		NextActionCodes          []string `json:"next_action_codes"`
		ExecutionAdmissionState  string   `json:"execution_admission_state"`
		DefaultWorkerLaunchState string   `json:"default_worker_launch_decision"`
	} `json:"decision"`
}

type gbrainWorkerLaunchDecisionExpectedRequest struct {
	TargetID     string
	Lane         string
	ActionCode   string
	WorkerSystem string
	WorkerRole   string
}

func gbrainWorkerLaunchDecisionEnabled() bool {
	value := strings.TrimSpace(strings.ToLower(os.Getenv(envGBrainWorkerLaunchDecisionEnabled)))
	switch value {
	case "1", "true", "yes", "on", "required":
		return true
	default:
		return false
	}
}

func gbrainWorkerLaunchDecisionEndpoint() string {
	base := strings.TrimSpace(os.Getenv(envGBrainWorkerLaunchDecisionURL))
	if base == "" {
		base = strings.TrimSpace(os.Getenv(envGBrainWorkerLaunchDecisionAPIBaseURL))
	}
	if base == "" {
		base = defaultGBrainWorkerLaunchDecisionBaseURL
	}
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(base, "/api/gbrain/worker-launch-decision") {
		return base
	}
	if strings.HasSuffix(base, "/api") {
		return base + "/gbrain/worker-launch-decision"
	}
	return base + "/api/gbrain/worker-launch-decision"
}

func gbrainWorkerLaunchDecisionLane() string {
	lane := strings.TrimSpace(os.Getenv(envGBrainWorkerLaunchDecisionLane))
	if lane == "" {
		return defaultGBrainWorkerLaunchDecisionLane
	}
	return lane
}

func gbrainWorkerLaunchDecisionTimeout() time.Duration {
	raw := strings.TrimSpace(os.Getenv(envGBrainWorkerLaunchDecisionTimeoutMS))
	if raw == "" {
		return time.Duration(defaultGBrainWorkerLaunchDecisionTimeoutMS) * time.Millisecond
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return time.Duration(defaultGBrainWorkerLaunchDecisionTimeoutMS) * time.Millisecond
	}
	return time.Duration(value) * time.Millisecond
}

func gbrainWorkerLaunchActionCodeFromText(text string) string {
	normalized := strings.ToUpper(text)
	normalized = strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '_'
	}, normalized)

	switch {
	case strings.Contains(normalized, "REPAIR_GBRAIN_WORKER_MEMORY_COVERAGE"):
		return "REPAIR_GBRAIN_WORKER_MEMORY_COVERAGE"
	case strings.Contains(normalized, "GBRAIN_WORKER_MEMORY_COVERAGE"):
		return "REPAIR_GBRAIN_WORKER_MEMORY_COVERAGE"
	case strings.Contains(normalized, "GBRAIN_WORKER_MEMORY_REPAIR"):
		return "REPAIR_GBRAIN_WORKER_MEMORY_COVERAGE"
	case strings.Contains(normalized, "GBRAIN_WORKER_MEMORY_RECOVERY"):
		return "REPAIR_GBRAIN_WORKER_MEMORY_COVERAGE"
	case strings.Contains(normalized, "GBRAINWORKERMEMORYGAP"):
		return "REPAIR_GBRAIN_WORKER_MEMORY_COVERAGE"
	case strings.Contains(normalized, "BACKFILL_OR_ARCHIVE_ORPHAN_WORKER_RUN"):
		return "REPAIR_GBRAIN_WORKER_MEMORY_COVERAGE"
	case strings.Contains(normalized, "RECONCILE_WORKER_RUN_LIFECYCLE"):
		return "REPAIR_GBRAIN_WORKER_MEMORY_COVERAGE"
	case strings.Contains(normalized, "REPAIR") && strings.Contains(normalized, "WORKER_MEMORY") && strings.Contains(normalized, "COVERAGE"):
		return "REPAIR_GBRAIN_WORKER_MEMORY_COVERAGE"
	case strings.Contains(normalized, "WORKER_MEMORY") && strings.Contains(normalized, "REPAIR"):
		return "REPAIR_GBRAIN_WORKER_MEMORY_COVERAGE"
	case strings.Contains(normalized, "WORKER_MEMORY") && strings.Contains(normalized, "RECOVERY"):
		return "REPAIR_GBRAIN_WORKER_MEMORY_COVERAGE"
	case strings.Contains(normalized, "ORPHAN") && strings.Contains(normalized, "WORKER_RUN"):
		return "REPAIR_GBRAIN_WORKER_MEMORY_COVERAGE"
	case strings.Contains(normalized, "WORKER_RUN") && strings.Contains(normalized, "LIFECYCLE"):
		return "REPAIR_GBRAIN_WORKER_MEMORY_COVERAGE"
	case strings.Contains(normalized, "COMPLETE_DRIVE_OAUTH"):
		return "COMPLETE_DRIVE_OAUTH"
	case strings.Contains(normalized, "DRIVE_OAUTH") || strings.Contains(normalized, "GOOGLE_DRIVE_OAUTH"):
		return "COMPLETE_DRIVE_OAUTH"
	case strings.Contains(normalized, "CAPTURE_GBRAIN_SOURCE_DOCUMENTS"):
		return "CAPTURE_GBRAIN_SOURCE_DOCUMENTS"
	case strings.Contains(normalized, "CAPTURE_GBRAIN_SOURCE_DOCUMENT"):
		return "CAPTURE_GBRAIN_SOURCE_DOCUMENTS"
	case strings.Contains(normalized, "GBRAIN_SOURCE_DOCUMENT_INTAKE"):
		return "CAPTURE_GBRAIN_SOURCE_DOCUMENTS"
	case strings.Contains(normalized, "SOURCE_DOCUMENT_INTAKE"):
		return "CAPTURE_GBRAIN_SOURCE_DOCUMENTS"
	case strings.Contains(normalized, "CAPTURE") && strings.Contains(normalized, "SOURCE") && strings.Contains(normalized, "DOCUMENT"):
		return "CAPTURE_GBRAIN_SOURCE_DOCUMENTS"
	case strings.Contains(normalized, "VALIDATE_GBRAIN_AUTONOMY_GATE"):
		return "VALIDATE_GBRAIN_AUTONOMY_GATE"
	case strings.Contains(normalized, "VALIDATE") && strings.Contains(normalized, "GBRAIN") && strings.Contains(normalized, "AUTONOMY"):
		return "VALIDATE_GBRAIN_AUTONOMY_GATE"
	case strings.Contains(normalized, "AUTONOMY") && strings.Contains(normalized, "GATE"):
		return "VALIDATE_GBRAIN_AUTONOMY_GATE"
	default:
		return ""
	}
}

func gbrainWorkerLaunchActionCodeFromBead(info *beadInfo, extraText ...string) string {
	if override := strings.TrimSpace(os.Getenv(envGBrainWorkerLaunchDecisionActionCode)); override != "" {
		return override
	}

	var parts []string
	if info != nil {
		parts = append(parts, info.Title, info.Description, info.IssueType, strings.Join(info.Labels, " "))
	}
	parts = append(parts, extraText...)
	return gbrainWorkerLaunchActionCodeFromText(strings.Join(parts, " "))
}

func gbrainWorkerLaunchDecisionRequestMismatches(decision gbrainWorkerLaunchDecisionResponse, expected gbrainWorkerLaunchDecisionExpectedRequest) []string {
	checks := []struct {
		name     string
		actual   string
		expected string
	}{
		{name: "target_id", actual: decision.Request.TargetID, expected: expected.TargetID},
		{name: "lane", actual: decision.Request.Lane, expected: expected.Lane},
		{name: "action_code", actual: decision.Request.ActionCode, expected: expected.ActionCode},
		{name: "worker_system", actual: decision.Request.WorkerSystem, expected: expected.WorkerSystem},
		{name: "worker_role", actual: decision.Request.WorkerRole, expected: expected.WorkerRole},
	}
	var mismatches []string
	for _, check := range checks {
		if check.actual != check.expected {
			mismatches = append(mismatches, fmt.Sprintf("%s: expected %s, got %s", check.name, defaultString(check.expected, "null"), defaultString(check.actual, "null")))
		}
	}
	return mismatches
}

func enforceGBrainWorkerLaunchDecision(req gbrainWorkerLaunchDecisionRequest) error {
	if !gbrainWorkerLaunchDecisionEnabled() {
		return nil
	}

	endpoint := gbrainWorkerLaunchDecisionEndpoint()
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		if err == nil {
			err = fmt.Errorf("missing URL scheme or host")
		}
		return fmt.Errorf("GBrain worker launch decision endpoint is invalid: %s: %w", endpoint, err)
	}

	query := parsed.Query()
	if req.TargetID != "" {
		query.Set("target_id", req.TargetID)
		query.Set("ops_work_id", req.TargetID)
	}
	lane := gbrainWorkerLaunchDecisionLane()
	query.Set("lane", lane)
	if req.WorkerSystem != "" {
		query.Set("worker_system", req.WorkerSystem)
	}
	if req.WorkerRole != "" {
		query.Set("worker_role", req.WorkerRole)
	}
	actionCode := strings.TrimSpace(req.ActionCode)
	if actionCode == "" {
		actionCode = gbrainWorkerLaunchActionCodeFromText(req.ActionText)
	}
	if actionCode != "" {
		query.Set("action_code", actionCode)
	}
	parsed.RawQuery = query.Encode()

	timeout := gbrainWorkerLaunchDecisionTimeout()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return fmt.Errorf("building GBrain worker launch decision request: %w", err)
	}
	httpReq.Header.Set("Accept", "application/json")

	response, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("GBrain worker launch decision request failed: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = response.Status
		}
		return fmt.Errorf("GBrain worker launch decision request failed with HTTP %d: %s", response.StatusCode, msg)
	}

	var decision gbrainWorkerLaunchDecisionResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&decision); err != nil {
		return fmt.Errorf("decoding GBrain worker launch decision response: %w", err)
	}
	if decision.Contract != "gbrain.worker_launch_decision.v1" || decision.Decision.Allowed == nil {
		return fmt.Errorf("GBrain worker launch decision response did not match gbrain.worker_launch_decision.v1")
	}
	mismatches := gbrainWorkerLaunchDecisionRequestMismatches(decision, gbrainWorkerLaunchDecisionExpectedRequest{
		TargetID:     req.TargetID,
		Lane:         lane,
		ActionCode:   actionCode,
		WorkerSystem: req.WorkerSystem,
		WorkerRole:   req.WorkerRole,
	})
	if len(mismatches) > 0 {
		return fmt.Errorf("GBrain worker launch decision response did not match request: %s", strings.Join(mismatches, "; "))
	}
	if *decision.Decision.Allowed {
		if len(decision.Decision.WarningReasonCodes) > 0 {
			fmt.Fprintf(os.Stderr, "GBrain launch warning: %s\n", strings.Join(decision.Decision.WarningReasonCodes, ","))
		}
		return nil
	}

	parts := []string{
		"GBrain worker launch decision denied dispatch",
		"state=" + defaultString(decision.Decision.State, "unknown"),
		"policy=" + defaultString(decision.Decision.PolicyMode, "unknown"),
		"recommendation=" + defaultString(decision.Decision.Recommendation, "unknown"),
	}
	if len(decision.Decision.BlockingReasonCodes) > 0 {
		parts = append(parts, "blockers="+strings.Join(decision.Decision.BlockingReasonCodes, ","))
	}
	if len(decision.Decision.NextActionCodes) > 0 {
		parts = append(parts, "next="+strings.Join(decision.Decision.NextActionCodes, ","))
	}
	return errors.New(strings.Join(parts, "; "))
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
