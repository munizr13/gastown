package polecat

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/util"
)

func diskRefusedManager(t *testing.T) *Manager {
	t.Helper()
	root := t.TempDir()
	m := NewManager(&rig.Rig{Name: "fixture", Path: root}, git.NewGit(root), nil)
	m.diskSpaceCheck = func(string) (util.DiskSpaceLevel, string, error) {
		return util.DiskSpaceCritical, "fixture disk is full", nil
	}
	return m
}

func TestAllocateAndAdd_DiskRefusalReleasesAllocation(t *testing.T) {
	m := diskRefusedManager(t)
	for attempt := 0; attempt < 3; attempt++ {
		name, worker, err := m.AllocateAndAdd(AddOptions{})
		if name != "" || worker != nil || !errors.Is(err, ErrDiskSpaceLow) {
			t.Fatalf("attempt %d: name=%q worker=%v error=%v", attempt, name, worker, err)
		}
		if m.namePool.ActiveCount() != 0 {
			t.Fatalf("refused spawn retained names: %v", m.namePool.ActiveNames())
		}
		entries, err := os.ReadDir(filepath.Join(m.rig.Path, "polecats"))
		if err != nil || len(entries) != 0 {
			t.Fatalf("refused spawn retained filesystem reservations: %v, %v", entries, err)
		}
	}
}

func TestAddWithOptions_DiskRefusalReleasesOnlyOwnedReservation(t *testing.T) {
	m := diskRefusedManager(t)
	name, err := m.AllocateName()
	if err != nil {
		t.Fatal(err)
	}
	other, err := m.AllocateName()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.AddWithOptions(name, AddOptions{}); !errors.Is(err, ErrDiskSpaceLow) {
		t.Fatalf("AddWithOptions: %v", err)
	}
	for _, path := range []string{m.polecatDir(name), m.pendingPath(name)} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("refused allocation remains at %s: %v", path, err)
		}
	}
	if _, err := os.Stat(m.pendingPath(other)); err != nil {
		t.Fatalf("unrelated reservation was changed: %v", err)
	}
	// A newly opened manager must be able to reuse the released name immediately.
	reopened := NewManager(m.rig, git.NewGit(m.rig.Path), nil)
	next, err := reopened.AllocateName()
	if err != nil || next != name {
		t.Fatalf("next allocation = %q, %v; wanted %q", next, err, name)
	}
}

func TestAddWithOptions_DiskRefusalPreservesOtherOwners(t *testing.T) {
	for _, owner := range []string{strconv.Itoa(os.Getpid() + 1), "unreadable-owner"} {
		t.Run(owner, func(t *testing.T) {
			m := diskRefusedManager(t)
			name, err := m.AllocateName()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(m.pendingPath(name), []byte(owner), 0644); err != nil {
				t.Fatal(err)
			}
			if _, err := m.AddWithOptions(name, AddOptions{}); !errors.Is(err, ErrDiskSpaceLow) {
				t.Fatalf("AddWithOptions: %v", err)
			}
			got, err := os.ReadFile(m.pendingPath(name))
			if err != nil || string(got) != owner || m.namePool.ActiveCount() != 1 {
				t.Fatalf("foreign reservation changed: %q, %v, names=%v", got, err, m.namePool.ActiveNames())
			}
		})
	}
}

func TestAddWithOptions_DiskRefusalDoesNotFollowReservationSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires additional privileges on Windows")
	}
	m := diskRefusedManager(t)
	name, err := m.AllocateName()
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "other-reservation")
	owner := strconv.Itoa(os.Getpid())
	if err := os.WriteFile(target, []byte(owner), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(m.pendingPath(name)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, m.pendingPath(name)); err != nil {
		t.Fatal(err)
	}
	_, err = m.AddWithOptions(name, AddOptions{})
	if !errors.Is(err, ErrDiskSpaceLow) || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("missing refusal or cleanup error: %v", err)
	}
	if _, err := os.Readlink(m.pendingPath(name)); err != nil {
		t.Fatalf("reservation link was removed: %v", err)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != owner {
		t.Fatalf("link target changed: %q, %v", got, err)
	}
}

func TestAddWithOptions_ExistingWorkerIsNotDiskRollback(t *testing.T) {
	m := diskRefusedManager(t)
	name, err := m.AllocateName()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(m.polecatDir(name), 0755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(m.polecatDir(name), "work.txt")
	if err := os.WriteFile(file, []byte("existing work"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AddWithOptions(name, AddOptions{}); !errors.Is(err, ErrPolecatExists) {
		t.Fatalf("AddWithOptions: %v", err)
	}
	if got, err := os.ReadFile(file); err != nil || string(got) != "existing work" {
		t.Fatalf("existing work changed: %q, %v", got, err)
	}
	if _, err := os.Stat(m.pendingPath(name)); err != nil || m.namePool.ActiveCount() != 1 {
		t.Fatalf("existing worker reservation changed: %v", err)
	}
}

func TestAllocateAndAdd_DiskRefusalPreservesUnexpectedContents(t *testing.T) {
	m := diskRefusedManager(t)
	var file string
	m.diskSpaceCheck = func(string) (util.DiskSpaceLevel, string, error) {
		names := m.namePool.ActiveNames()
		if len(names) != 1 {
			t.Fatalf("expected one allocated name: %v", names)
		}
		file = filepath.Join(m.polecatDir(names[0]), "unexpected.txt")
		if err := os.WriteFile(file, []byte("preserve"), 0644); err != nil {
			t.Fatal(err)
		}
		return util.DiskSpaceCritical, "fixture disk is full", nil
	}
	_, _, err := m.AllocateAndAdd(AddOptions{})
	if !errors.Is(err, ErrDiskSpaceLow) || !strings.Contains(err.Error(), "removing refused spawn directory") {
		t.Fatalf("missing refusal or incomplete cleanup error: %v", err)
	}
	if got, err := os.ReadFile(file); err != nil || string(got) != "preserve" {
		t.Fatalf("unexpected data was deleted: %q, %v", got, err)
	}
}

func TestAddWithOptions_DiskRefusalPreservesConcurrentOverflowCounter(t *testing.T) {
	m := diskRefusedManager(t)
	m.namePool.MaxSize = 1
	name, err := m.AllocateName()
	if err != nil {
		t.Fatal(err)
	}
	other := NewManager(m.rig, git.NewGit(m.rig.Path), nil)
	if _, err := other.AllocateName(); err != nil {
		t.Fatal(err)
	}
	expected := other.namePool.OverflowNext
	if expected == m.namePool.OverflowNext {
		t.Fatal("fixture did not advance the second allocator's overflow counter")
	}
	if _, err := m.AddWithOptions(name, AddOptions{}); !errors.Is(err, ErrDiskSpaceLow) {
		t.Fatalf("AddWithOptions: %v", err)
	}
	data, err := os.ReadFile(m.namePool.stateFile)
	if err != nil {
		t.Fatal(err)
	}
	var state namePoolState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	if state.OverflowNext != expected {
		t.Fatalf("rollback overwrote another allocation: next=%d, want %d", state.OverflowNext, expected)
	}
}

func TestAddWithOptions_DiskAdmissionExcludesStaleReservationReaping(t *testing.T) {
	m := diskRefusedManager(t)
	name, err := m.AllocateName()
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * pendingMaxAge)
	if err := os.Chtimes(m.pendingPath(name), old, old); err != nil {
		t.Fatal(err)
	}
	reaper := NewManager(m.rig, git.NewGit(m.rig.Path), nil)
	reaped := make(chan struct{})
	m.diskSpaceCheck = func(string) (util.DiskSpaceLevel, string, error) {
		started := make(chan struct{})
		go func() {
			close(started)
			reaper.ReconcilePool()
			close(reaped)
		}()
		<-started
		select {
		case <-reaped:
			t.Error("stale-marker reaper crossed an active spawn admission")
		case <-time.After(100 * time.Millisecond):
		}
		if _, err := os.Stat(m.pendingPath(name)); err != nil {
			t.Errorf("active admission lost its reservation: %v", err)
		}
		return util.DiskSpaceCritical, "fixture disk is full", nil
	}
	if _, err := m.AddWithOptions(name, AddOptions{}); !errors.Is(err, ErrDiskSpaceLow) {
		t.Errorf("AddWithOptions: %v", err)
	}
	select {
	case <-reaped:
	case <-time.After(5 * time.Second):
		t.Fatal("reservation cleanup deadlocked with the reaper")
	}
	if _, err := os.Stat(m.pendingPath(name)); !os.IsNotExist(err) {
		t.Fatalf("refused reservation remains: %v", err)
	}
}
