package daemon

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/formula"
)

func receiptShellWord(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

// Execute real daemon entry points with a local receipt recorder and synthetic
// Dolt output. Git work stays in the temporary archive, which has no remotes.
func maintenancePipelineFixture(t *testing.T, formulaName string) (*Daemon, string, string) {
	t.Helper()
	dir := t.TempDir()
	content, err := formula.GetEmbeddedFormulaContent(formulaName)
	if err != nil {
		t.Fatal(err)
	}
	definition, err := formula.Parse(content)
	if err != nil {
		t.Fatal(err)
	}
	receipts := filepath.Join(dir, "receipts")
	var script strings.Builder
	script.WriteString("#!/bin/sh\ncase \"$1\" in\nmol) echo 'Spawned wisp: fx-wisp-root';;\nshow) printf '['\n")
	for i, step := range definition.Steps {
		prefix := ""
		if i > 0 {
			prefix = ","
		}
		id, _ := json.Marshal(step.ID)
		title, _ := json.Marshal(step.Title)
		fmt.Fprintf(&script, "printf '%%s' %s\n", receiptShellWord(prefix+`{"id":`+string(id)+`,"title":`+string(title)+`,"status":"`))
		fmt.Fprintf(&script, "if [ -f %s ]; then printf closed; else printf open; fi\nprintf '\"}'\n", receiptShellWord(filepath.Join(dir, "closed-"+step.ID)))
	}
	script.WriteString("printf ']\\n';;\nclose) printf '%s\\n' \"$*\" >> " + receiptShellWord(receipts) + "\ntouch " + receiptShellWord(dir) + "/closed-\"$2\";;\nesac\n")
	bd := filepath.Join(dir, "bd")
	if err := os.WriteFile(bd, []byte(script.String()), 0755); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(dir, "archive")
	for _, name := range []string{archive, filepath.Join(dir, ".dolt-data")} {
		if err := os.Mkdir(name, 0755); err != nil {
			t.Fatal(err)
		}
	}
	mustRunGit(t, archive, "init")
	mustRunGit(t, archive, "config", "user.name", "Synthetic")
	mustRunGit(t, archive, "config", "user.email", "synthetic@example.invalid")
	dolt := `#!/bin/sh
case "$*" in
  *broken*) exit 1;;
  *comments*) if [ "$FIXTURE_FAIL_COMMENTS" = 1 ]; then exit 1; fi;;
esac
case "$*" in
  *issues*) echo '{"rows":[{"id":"fx-abc123","title":"Fixture work"}]}';;
  *) echo '{"rows":[]}';;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "dolt"), []byte(dolt), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	d := &Daemon{config: &Config{TownRoot: dir}, bdPath: bd, gtPath: filepath.Join(dir, "missing-gt"), logger: log.New(io.Discard, "", 0)}
	return d, archive, receipts
}

func TestJSONLBackupPipelineReportsPartialExports(t *testing.T) {
	for _, mode := range []string{"complete", "database-failure", "supplemental-failure"} {
		t.Run(mode, func(t *testing.T) {
			d, archive, receiptFile := maintenancePipelineFixture(t, constants.MolDogJSONL)
			databases := []string{"fixture"}
			if mode == "database-failure" {
				databases = append(databases, "broken")
			}
			t.Setenv("FIXTURE_FAIL_COMMENTS", map[bool]string{true: "1", false: "0"}[mode == "supplemental-failure"])
			d.patrolConfig = &DaemonPatrolConfig{Patrols: &PatrolsConfig{JsonlGitBackup: &JsonlGitBackupConfig{Enabled: true, GitRepo: archive, Databases: databases}}}
			d.syncJsonlGitBackup()
			data, err := os.ReadFile(receiptFile)
			if err != nil {
				t.Fatal(err)
			}
			receipts := string(data)
			if mode == "complete" {
				for _, want := range []string{"close export --reason export returned: databases=1", "close verify --reason post-scrub verification returned no suspicious records", "close push --reason backup helper returned"} {
					if !strings.Contains(receipts, want) {
						t.Fatalf("missing scoped success %q: %s", want, receipts)
					}
				}
				if _, err := runGitCmd(archive, "show", "HEAD:fixture/issues.jsonl"); err != nil {
					t.Fatal(err)
				}
			} else {
				if !strings.Contains(receipts, "close export --reason failed:") {
					t.Fatalf("partial backup claimed success: %s", receipts)
				}
				if _, err := runGitCmd(archive, "rev-parse", "HEAD"); err == nil {
					t.Fatal("incomplete first backup reached commit")
				}
				if mode == "database-failure" && !strings.Contains(receipts, "close verify --reason failed: verify broken:") {
					t.Fatalf("missing file passed verification: %s", receipts)
				}
			}
		})
	}
}

func TestJSONLExporterRequiresRowsBeforeReplacingExistingBackup(t *testing.T) {
	for _, output := range []string{`{}`, `{"rows":null}`, `{"error":"fixture failure"}`} {
		t.Run(output, func(t *testing.T) {
			dir := t.TempDir()
			prior := filepath.Join(dir, "issues.jsonl")
			if err := os.WriteFile(prior, []byte("retained backup\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "dolt"), []byte("#!/bin/sh\nprintf '%s\\n' "+receiptShellWord(output)+"\n"), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			d := &Daemon{}
			if _, err := d.exportTableToJsonl("issues", "SELECT fixture", dir, dir); err == nil {
				t.Fatal("missing rows passed as an empty export")
			}
			data, err := os.ReadFile(prior)
			if err != nil || string(data) != "retained backup\n" {
				t.Fatalf("prior backup was replaced: %s %v", data, err)
			}
		})
	}
}

func TestJSONLBackupPipelineDoesNotVerifyRejectedCountDrop(t *testing.T) {
	d, archive, receiptFile := maintenancePipelineFixture(t, constants.MolDogJSONL)
	if err := os.Mkdir(filepath.Join(archive, "fixture"), 0755); err != nil {
		t.Fatal(err)
	}
	var baseline strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&baseline, "{\"id\":\"fx-%06d\",\"title\":\"Fixture work\"}\n", i)
	}
	if err := os.WriteFile(filepath.Join(archive, "fixture", "issues.jsonl"), []byte(baseline.String()), 0600); err != nil {
		t.Fatal(err)
	}
	mustRunGit(t, archive, "add", ".")
	mustRunGit(t, archive, "commit", "-m", "Synthetic baseline")
	before, err := runGitCmd(archive, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	d.patrolConfig = &DaemonPatrolConfig{Patrols: &PatrolsConfig{JsonlGitBackup: &JsonlGitBackupConfig{Enabled: true, GitRepo: archive, Databases: []string{"fixture"}}}}
	d.syncJsonlGitBackup()
	data, err := os.ReadFile(receiptFile)
	if err != nil {
		t.Fatal(err)
	}
	receipts := string(data)
	if !strings.Contains(receipts, "close verify --reason failed: export count spike detected") || strings.Contains(receipts, "close verify --reason post-scrub") {
		t.Fatalf("rejected count drop received successful verification: %s", receipts)
	}
	after, err := runGitCmd(archive, "rev-parse", "HEAD")
	if err != nil || after != before {
		t.Fatalf("rejected backup changed HEAD: before=%s after=%s err=%v", before, after, err)
	}
}

func TestJSONLVerificationRejectsUnreadableMalformedAndTruncatedInput(t *testing.T) {
	for _, tc := range []struct {
		name, data string
		missing    bool
	}{
		{"missing", "", true}, {"malformed", "{bad}\n", false}, {"null", "null\n", false},
		{"oversize-record", `{"title":"` + strings.Repeat("x", 1024*1024) + `"}` + "\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Mkdir(filepath.Join(dir, "fixture"), 0755); err != nil {
				t.Fatal(err)
			}
			if !tc.missing {
				if err := os.WriteFile(filepath.Join(dir, "fixture", "issues.jsonl"), []byte(tc.data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			d := &Daemon{logger: log.New(io.Discard, "", 0)}
			if _, err := d.verifyNoPollution(dir, []string{"fixture"}); err == nil {
				t.Fatal("unverified input passed as clean")
			}
		})
	}
}

func TestCompactorPipelineCannotVerifyFailedInspection(t *testing.T) {
	d, _, receiptFile := maintenancePipelineFixture(t, constants.MolDogCompactor)
	d.patrolConfig = &DaemonPatrolConfig{Patrols: &PatrolsConfig{CompactorDog: &CompactorDogConfig{Enabled: true, Databases: []string{"invalid database"}}}}
	d.runCompactorDog()
	data, err := os.ReadFile(receiptFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"close compact --reason failed: compacted=0, skipped=0, errors=1", "close verify --reason skipped: no database completed compaction"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("failed inspection yielded misleading receipt: %s", data)
		}
	}
}
