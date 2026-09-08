package daemon

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/constants"
)

func TestParseWispID(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		wantID string
	}{
		{
			name:   "standard wisp output",
			input:  "✓ Spawned wisp: gt-wisp-abc123 — Reap stale wisps",
			wantID: "gt-wisp-abc123",
		},
		{
			name:   "wisp ID with ANSI codes",
			input:  "\033[32m✓\033[0m Spawned wisp: \033[1mgt-wisp-xyz789\033[0m — Title",
			wantID: "gt-wisp-xyz789",
		},
		{
			name:   "empty output",
			input:  "",
			wantID: "",
		},
		{
			name:   "no wisp ID in output",
			input:  "Error: something went wrong",
			wantID: "",
		},
		{
			name:   "wisp ID at end of line",
			input:  "Created gt-wisp-def456",
			wantID: "gt-wisp-def456",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseWispID(tt.input)
			if got != tt.wantID {
				t.Errorf("parseWispID(%q) = %q, want %q", tt.input, got, tt.wantID)
			}
		})
	}
}

func TestStripANSI(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no ANSI", "hello", "hello"},
		{"color code", "\033[32mgreen\033[0m", "green"},
		{"bold", "\033[1mbold\033[0m", "bold"},
		{"multiple codes", "\033[32m✓\033[0m \033[1mtext\033[0m", "✓ text"},
		{"empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripANSI(tt.input)
			if got != tt.want {
				t.Errorf("stripANSI(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseChildrenJSON(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantIDs []string
		wantErr bool
	}{
		{
			name:    "bare array",
			input:   `[{"id":"a","title":"Probe","status":"open"}]`,
			wantIDs: []string{"a"},
		},
		{
			name:    "map wrapper from bd show",
			input:   `{"hq-wisp-root":[{"id":"hq-wisp-a","title":"Probe","status":"open"},{"id":"hq-wisp-b","title":"Report","status":"open"}]}`,
			wantIDs: []string{"hq-wisp-a", "hq-wisp-b"},
		},
		{
			name:    "empty map wrapper",
			input:   `{"hq-wisp-root":[]}`,
			wantIDs: []string{},
		},
		{
			name:    "schema metadata with children",
			input:   `{"hq-wisp-root":[{"id":"hq-wisp-a","title":"Probe","status":"open"}],"schema_version":1}`,
			wantIDs: []string{"hq-wisp-a"},
		},
		{
			name:    "schema metadata with empty children",
			input:   `{"hq-wisp-root":[],"schema_version":1}`,
			wantIDs: []string{},
		},
		{
			name:    "multiple child arrays are deterministic",
			input:   `{"hq-wisp-b":[{"id":"b-step","title":"Report","status":"open"}],"schema_version":1,"hq-wisp-a":[{"id":"a-step","title":"Probe","status":"open"}]}`,
			wantIDs: []string{"a-step", "b-step"},
		},
		{
			name:    "schema key is metadata even if array-valued",
			input:   `{"schema_version":[{"id":"metadata","title":"Ignore","status":"open"}],"hq-wisp-root":[{"id":"hq-wisp-a","title":"Probe","status":"open"}]}`,
			wantIDs: []string{"hq-wisp-a"},
		},
		{
			name:    "empty array",
			input:   `[]`,
			wantIDs: []string{},
		},
		{
			name:    "empty input",
			input:   `   `,
			wantErr: true,
		},
		{
			name:    "malformed bare array",
			input:   `[`,
			wantErr: true,
		},
		{
			name:    "malformed object envelope",
			input:   `{"hq-wisp-root":[`,
			wantErr: true,
		},
		{
			name:    "invalid json",
			input:   `not json`,
			wantErr: true,
		},
		{
			name:    "malformed child array",
			input:   `{"hq-wisp-root":[{"id":1}],"schema_version":1}`,
			wantErr: true,
		},
		{
			name:    "non-array child payload",
			input:   `{"hq-wisp-root":1,"schema_version":1}`,
			wantErr: true,
		},
		{
			name:    "metadata only is not silent skip-all",
			input:   `{"schema_version":1}`,
			wantErr: true,
		},
		{
			name:    "empty object is not silent skip-all",
			input:   `{}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseChildrenJSON(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			gotIDs := make([]string, 0, len(got))
			for _, child := range got {
				gotIDs = append(gotIDs, child.ID)
			}
			if !reflect.DeepEqual(gotIDs, tt.wantIDs) {
				t.Errorf("got child IDs %v, want %v", gotIDs, tt.wantIDs)
			}
		})
	}
}

func TestDogMolGracefulDegradation(t *testing.T) {
	// A dogMol with empty rootID should be a no-op for all operations.
	dm := &dogMol{
		rootID:  "",
		stepIDs: make(map[string]string),
	}

	// These should not panic or error — graceful degradation.
	dm.closeStep("scan")
	dm.failStep("scan", "test failure")
	dm.close()
}

func TestCloseRemainingStepsMultiPassAndForce(t *testing.T) {
	// Locks the closeRemainingSteps contract: a step blocked by another open
	// step closes on a later pass once its blocker closes, and steps plain
	// close can never resolve (cyclic deps) are force-closed. Regression for
	// the ~13 orphan wisps/hour accretion (single arbitrary-order pass).
	stateDir := t.TempDir()

	// Fake bd: A closes plainly; B closes only after A; C and D are cyclic
	// (plain close always fails) and only close with --force.
	script := `#!/bin/sh
STATE='` + stateDir + `'
cmd="$1"; shift
case "$cmd" in
show)
  out="["; sep=""
  for w in A B C D; do
    if [ -f "$STATE/closed-$w" ]; then st="closed"; else st="open"; fi
    out="$out$sep{\"id\":\"$w\",\"title\":\"step $w\",\"status\":\"$st\"}"
    sep=","
  done
  echo "$out]"
  ;;
close)
  id="$1"; shift
  force=0
  for a in "$@"; do [ "$a" = "--force" ] && force=1; done
  if [ "$force" = "1" ]; then touch "$STATE/closed-$id"; exit 0; fi
  case "$id" in
    A) touch "$STATE/closed-A"; exit 0;;
    B) if [ -f "$STATE/closed-A" ]; then touch "$STATE/closed-B"; exit 0; fi
       echo "cannot close B: blocked by open issues [A]" >&2; exit 1;;
    C|D) echo "cannot close $id: blocked by open issues (cycle)" >&2; exit 1;;
  esac
  ;;
esac
exit 0
`
	bdFake := filepath.Join(stateDir, "bd-fake")
	if err := os.WriteFile(bdFake, []byte(script), 0755); err != nil {
		t.Fatalf("writing fake bd: %v", err)
	}

	dm := &dogMol{
		rootID:   "root",
		stepIDs:  map[string]string{},
		bdPath:   bdFake,
		townRoot: stateDir,
		logger:   log.New(io.Discard, "", 0),
	}

	if err := dm.closeRemainingSteps(); err != nil {
		t.Fatal(err)
	}

	for _, w := range []string{"A", "B", "C", "D"} {
		if _, err := os.Stat(filepath.Join(stateDir, "closed-"+w)); err != nil {
			t.Errorf("step %s was not closed (multi-pass/force contract broken)", w)
		}
	}
}

func TestDogMoleculeRetirementCannotClaimUnexecutedStepsSucceeded(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/sh
STATE='` + dir + `'
cmd="$1"; shift
case "$cmd" in
show) echo '[{"id":"unrun-purge","title":"Purge","status":"open"}]';;
close) printf '%s\n' "$*" >> "$STATE/receipts";;
esac
`
	bd := filepath.Join(dir, "bd")
	if err := os.WriteFile(bd, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	dm := &dogMol{rootID: "root", stepIDs: map[string]string{"scan": "scan", "reap": "reap"}, bdPath: bd, townRoot: dir, logger: log.New(io.Discard, "", 0)}
	dm.closeStep("scan")
	dm.failStep("reap", "database unavailable")
	dm.close()
	data, err := os.ReadFile(filepath.Join(dir, "receipts"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"scan --reason completed: daemon reported step finished",
		"reap --reason failed: database unavailable",
		"unrun-purge --reason canceled: no execution receipt",
		"root --reason retired: daemon tracking ended; root closure does not prove step execution",
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("missing receipt %q: %s", want, data)
		}
	}
}

func TestDogMoleculeRetainsRootWhenChildRetirementIsUnconfirmed(t *testing.T) {
	for _, children := range []string{
		"read-failure", "invalid-json", "{}",
		`{"another-root":[]}`,
		`[{"id":"step","status":"unexpected"}]`,
		`[{"id":"step","status":"open"}]`,
	} {
		t.Run(children, func(t *testing.T) {
			dir := t.TempDir()
			show := "echo '" + children + "'"
			if children == "read-failure" {
				show = "exit 1"
			}
			script := "#!/bin/sh\ncase \"$1\" in\nshow) " + show + ";;\nclose)\n" +
				"if [ \"$2\" = root ]; then touch '" + filepath.Join(dir, "root-closed") + "'; exit 0; fi\nexit 1;;\nesac\n"
			bd := filepath.Join(dir, "bd")
			if err := os.WriteFile(bd, []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			dm := &dogMol{rootID: "root", bdPath: bd, townRoot: dir, logger: log.New(io.Discard, "", 0)}
			dm.close()
			if _, err := os.Stat(filepath.Join(dir, "root-closed")); !os.IsNotExist(err) {
				t.Fatal("root retired without confirmed child retirement")
			}
		})
	}
}

func TestDogStepIdentityDoesNotConfuseBackupOrVerifyTitles(t *testing.T) {
	for _, tc := range []struct {
		name     string
		children []childInfo
		want     map[string]string
	}{
		{constants.MolDogBackup, []childInfo{
			{ID: "sync-id", Title: "Sync databases to backup remotes"},
			{ID: "offsite-id", Title: "Sync backups to offsite storage"},
		}, map[string]string{"sync": "sync-id", "offsite": "offsite-id"}},
		{constants.MolDogJSONL, []childInfo{
			{ID: "export-id", Title: "Export databases to JSONL"},
			{ID: "verify-id", Title: "Verify export counts and filter pollution"},
		}, map[string]string{"export": "export-id", "verify": "verify-id"}},
		{constants.MolDogJSONL, []childInfo{
			{ID: "ambiguous-1", Title: "Verify export counts and filter pollution"},
			{ID: "ambiguous-2", Title: "Verify export counts and filter pollution"},
			{ID: "unknown", Title: "Export something else"},
		}, map[string]string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			payload, err := json.Marshal(tc.children)
			if err != nil {
				t.Fatal(err)
			}
			bd := filepath.Join(dir, "bd")
			if err := os.WriteFile(bd, []byte("#!/bin/sh\necho '"+string(payload)+"'\n"), 0755); err != nil {
				t.Fatal(err)
			}
			dm := &dogMol{rootID: "root", formulaName: tc.name, bdPath: bd, townRoot: dir,
				stepIDs: map[string]string{"stale": "must-not-reuse"}, logger: log.New(io.Discard, "", 0)}
			dm.discoverSteps()
			if !reflect.DeepEqual(dm.stepIDs, tc.want) {
				t.Fatalf("mapped wrong execution receipt: got %v want %v", dm.stepIDs, tc.want)
			}
		})
	}
}

func TestDogMoleculeCancelsBlockedAndDeferredSteps(t *testing.T) {
	dir := t.TempDir()
	bd := filepath.Join(dir, "bd")
	script := "#!/bin/sh\ncase \"$1\" in\nshow) echo '[{\"id\":\"blocked-step\",\"status\":\"blocked\"},{\"id\":\"deferred-step\",\"status\":\"deferred\"}]';;\n" +
		"close) printf '%s\\n' \"$*\" >> '" + filepath.Join(dir, "receipts") + "';;\nesac\n"
	if err := os.WriteFile(bd, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	dm := &dogMol{rootID: "root", bdPath: bd, townRoot: dir, logger: log.New(io.Discard, "", 0)}
	dm.close()
	data, err := os.ReadFile(filepath.Join(dir, "receipts"))
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []string{"blocked-step", "deferred-step"} {
		if !strings.Contains(string(data), step+" --reason canceled: no execution receipt") {
			t.Fatalf("missing explicit cancellation for %s: %s", step, data)
		}
	}
}
