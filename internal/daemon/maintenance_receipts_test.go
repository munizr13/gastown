package daemon

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckpointFailureIsNotReportedAsCleanWork(t *testing.T) {
	d := &Daemon{logger: log.New(io.Discard, "", 0)}
	dir := t.TempDir()
	if created, err := d.checkpointWorktree(dir, "test", "test"); created || err == nil {
		t.Fatalf("not a repository: created=%t err=%v", created, err)
	}
	mustRunGit(t, dir, "init")
	mustRunGit(t, dir, "config", "user.name", "Synthetic")
	mustRunGit(t, dir, "config", "user.email", "synthetic@example.invalid")
	if created, err := d.checkpointWorktree(dir, "test", "test"); created || err != nil {
		t.Fatalf("clean repository: created=%t err=%v", created, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "work.txt"), []byte("retain this work"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "hooks", "pre-commit"), []byte("#!/bin/sh\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if created, err := d.checkpointWorktree(dir, "test", "test"); created || err == nil {
		t.Fatalf("failed commit: created=%t err=%v", created, err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "work.txt")); err != nil || string(data) != "retain this work" {
		t.Fatal("failed checkpoint lost work")
	}
}

func TestCheckpointScanDistinguishesAbsentWorkersFromReadFailure(t *testing.T) {
	dir := t.TempDir()
	d := &Daemon{config: &Config{TownRoot: dir}, logger: log.New(io.Discard, "", 0)}
	if scanned, saved, failures := d.checkpointRigPolecats("test"); scanned != 0 || saved != 0 || failures != 0 {
		t.Fatalf("unused rig: %d %d %d", scanned, saved, failures)
	}
	if err := os.Mkdir(filepath.Join(dir, "test"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "test", "polecats"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if scanned, saved, failures := d.checkpointRigPolecats("test"); scanned != 0 || saved != 0 || failures != 1 {
		t.Fatalf("unreadable worker directory: %d %d %d", scanned, saved, failures)
	}
}

func TestCheckpointPreservesDeletedFilenameWithNewline(t *testing.T) {
	dir := t.TempDir()
	mustRunGit(t, dir, "init")
	mustRunGit(t, dir, "config", "user.name", "Synthetic")
	mustRunGit(t, dir, "config", "user.email", "synthetic@example.invalid")
	deleted := "retain\nthis.txt"
	if err := os.WriteFile(filepath.Join(dir, deleted), []byte("retained in history"), 0600); err != nil {
		t.Fatal(err)
	}
	mustRunGit(t, dir, "add", ".")
	mustRunGit(t, dir, "commit", "-m", "fixture")
	if err := os.Remove(filepath.Join(dir, deleted)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new work"), 0600); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{logger: log.New(io.Discard, "", 0)}
	if created, err := d.checkpointWorktree(dir, "fixture", "worker"); !created || err != nil {
		t.Fatalf("checkpoint: created=%t err=%v", created, err)
	}
	if out, err := runGitCmd(dir, "show", "HEAD:"+deleted); err != nil || out != "retained in history" {
		t.Fatalf("checkpoint committed a deletion: %q %v", out, err)
	}
	if out, err := runGitCmd(dir, "show", "HEAD:new.txt"); err != nil || out != "new work" {
		t.Fatalf("checkpoint did not preserve new work: %q %v", out, err)
	}
}

func TestCheckpointInspectionFailureCannotReachCommit(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	for _, failure := range []string{"deletions", "unstage", "staged-diff"} {
		t.Run(failure, func(t *testing.T) {
			dir, bin := t.TempDir(), t.TempDir()
			mustRunGit(t, dir, "init")
			mustRunGit(t, dir, "config", "user.name", "Synthetic")
			mustRunGit(t, dir, "config", "user.email", "synthetic@example.invalid")
			if err := os.WriteFile(filepath.Join(dir, "old.txt"), []byte("old"), 0600); err != nil {
				t.Fatal(err)
			}
			mustRunGit(t, dir, "add", ".")
			mustRunGit(t, dir, "commit", "-m", "fixture")
			before, err := runGitCmd(dir, "rev-parse", "HEAD")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(dir, "old.txt")); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new"), 0600); err != nil {
				t.Fatal(err)
			}
			pattern := map[string]string{
				"deletions":   "diff --cached --name-only --diff-filter=D -z",
				"unstage":     "reset HEAD -- old.txt",
				"staged-diff": "diff --cached --quiet",
			}[failure]
			script := fmt.Sprintf("#!/bin/sh\nif [ \"$*\" = %q ]; then echo fixture-failure >&2; exit 128; fi\nexec %q \"$@\"\n", pattern, git)
			if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			d := &Daemon{logger: log.New(io.Discard, "", 0)}
			if created, err := d.checkpointWorktree(dir, "fixture", "worker"); created || err == nil {
				t.Fatalf("failed inspection reached success: created=%t err=%v", created, err)
			}
			if after, err := runGitCmd(dir, "rev-parse", "HEAD"); err != nil || after != before {
				t.Fatalf("failed inspection committed: %q %v", after, err)
			}
		})
	}
}

func TestOffsiteReceiptRequiresSuccessfulLocalRsync(t *testing.T) {
	for _, exitCode := range []int{0, 1} {
		t.Run(fmt.Sprint(exitCode), func(t *testing.T) {
			dir := t.TempDir()
			source, target := filepath.Join(dir, "source"), filepath.Join(dir, "target")
			if err := os.Mkdir(source, 0755); err != nil {
				t.Fatal(err)
			}
			args := filepath.Join(dir, "args")
			fake := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + args + "'\nexit " + fmt.Sprint(exitCode) + "\n"
			if err := os.WriteFile(filepath.Join(dir, "rsync"), []byte(fake), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			d := &Daemon{logger: log.New(io.Discard, "", 0)}
			err := d.syncOffsiteBackupTo(source, target)
			if (err != nil) != (exitCode != 0) {
				t.Fatalf("rsync exit=%d error=%v", exitCode, err)
			}
			data, readErr := os.ReadFile(args)
			if readErr != nil || !strings.Contains(string(data), source+"/\n"+target+"/\n") {
				t.Fatalf("unexpected rsync scope: %s (%v)", data, readErr)
			}
		})
	}
}

func TestOffsiteUnavailableSourceCannotReturnSuccess(t *testing.T) {
	d := &Daemon{logger: log.New(io.Discard, "", 0)}
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := d.syncOffsiteBackupTo(filepath.Join(dir, "missing"), target); err == nil {
		t.Fatal("missing backup was reported successful")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("unavailable backup mutated destination")
	}
}
