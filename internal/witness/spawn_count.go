package witness

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/steveyegge/gastown/internal/atomicfile"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/lock"
	"github.com/steveyegge/gastown/internal/workspace"
)

// respawnMu serializes in-process access to the respawn state file.
// Cross-process serialization is handled by lock.FlockAcquire on a
// sibling .flock file (see RecordBeadRespawn, ShouldBlockRespawn, etc.).
var respawnMu sync.Mutex

// beadRespawnRecord tracks how many times a single bead has been reset for re-dispatch.
type beadRespawnRecord struct {
	BeadID      string    `json:"bead_id"`
	Count       int       `json:"count"`
	LastRespawn time.Time `json:"last_respawn"`
}

// beadRespawnState holds respawn counts for all tracked beads.
type beadRespawnState struct {
	Beads       map[string]*beadRespawnRecord `json:"beads"`
	LastUpdated time.Time                     `json:"last_updated"`
}

func beadRespawnStateFile(townRoot string) string {
	return filepath.Join(townRoot, "witness", "bead-respawn-counts.json")
}

func loadBeadRespawnState(townRoot string) (*beadRespawnState, error) {
	data, err := os.ReadFile(beadRespawnStateFile(townRoot))
	if os.IsNotExist(err) {
		if _, statErr := os.Lstat(beadRespawnStateFile(townRoot)); statErr == nil {
			return nil, err
		} else if !os.IsNotExist(statErr) {
			return nil, statErr
		}
		return &beadRespawnState{Beads: make(map[string]*beadRespawnRecord)}, nil
	}
	if err != nil {
		return nil, err
	}
	var state beadRespawnState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	if state.Beads == nil {
		return nil, fmt.Errorf("respawn state is missing beads")
	}
	for id, rec := range state.Beads {
		if rec == nil || rec.Count < 0 || rec.BeadID != id {
			return nil, fmt.Errorf("invalid respawn record for %s", id)
		}
	}
	return &state, nil
}

func lockBeadRespawnState(townRoot string) (func(), error) {
	path := beadRespawnStateFile(townRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	return lock.FlockAcquire(path + ".flock")
}

func saveBeadRespawnState(townRoot string, state *beadRespawnState) error {
	stateFile := beadRespawnStateFile(townRoot)
	if err := os.MkdirAll(filepath.Dir(stateFile), 0755); err != nil {
		return fmt.Errorf("creating witness dir: %w", err)
	}
	state.LastUpdated = time.Now().UTC()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling respawn state: %w", err)
	}
	return atomicfile.WriteFile(stateFile, data, 0600)
}

// ShouldBlockRespawn returns true if the bead has already been respawned
// MaxBeadRespawns times (from operational config). When true, the caller
// should escalate to mayor instead of sending RECOVERED_BEAD to deacon
// for re-dispatch. This is the primary circuit breaker for spawn storms
// (clown show #22).
func ShouldBlockRespawn(workDir, beadID string) bool {
	respawnMu.Lock()
	defer respawnMu.Unlock()

	townRoot, err := workspace.Find(workDir)
	if err != nil || townRoot == "" {
		townRoot = workDir
	}
	maxRespawns := config.LoadOperationalConfig(townRoot).GetWitnessConfig().MaxBeadRespawnsV()

	unlock, err := lockBeadRespawnState(townRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "respawn check blocked: %v\n", err)
		return true
	}
	defer unlock()
	state, err := loadBeadRespawnState(townRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "respawn check blocked: %v\n", err)
		return true
	}
	rec, ok := state.Beads[beadID]
	if !ok {
		return false
	}
	return rec.Count >= maxRespawns
}

// ReserveBeadRespawn checks and consumes one startup attempt under the same
// process/file lock. A failed or interrupted launch still consumes its attempt;
// only explicit investigation and reset reopen the shared budget.
func ReserveBeadRespawn(workDir, beadID string) (int, error) {
	return recordBeadRespawn(workDir, beadID, true)
}

// RecordBeadRespawn records legacy witness recovery attempts. A negative result
// means persistence failed; callers must not re-dispatch that recovery.
func RecordBeadRespawn(workDir, beadID string) int {
	count, err := recordBeadRespawn(workDir, beadID, false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "respawn recording blocked: %v\n", err)
		return -1
	}
	return count
}

func recordBeadRespawn(workDir, beadID string, enforceLimit bool) (int, error) {
	respawnMu.Lock()
	defer respawnMu.Unlock()
	townRoot, err := workspace.Find(workDir)
	if err != nil || townRoot == "" {
		townRoot = workDir
	}
	unlock, err := lockBeadRespawnState(townRoot)
	if err != nil {
		return 0, err
	}
	defer unlock()
	state, err := loadBeadRespawnState(townRoot)
	if err != nil {
		return 0, err
	}
	rec := state.Beads[beadID]
	if rec == nil {
		rec = &beadRespawnRecord{BeadID: beadID}
		state.Beads[beadID] = rec
	}
	limit := config.LoadOperationalConfig(townRoot).GetWitnessConfig().MaxBeadRespawnsV()
	if enforceLimit && rec.Count >= limit {
		return rec.Count, fmt.Errorf("respawn limit reached for %s (%d attempts); investigate before re-dispatching. Reset only after containing automatic feeders: gt sling respawn-reset %s", beadID, limit, beadID)
	}
	rec.Count++
	rec.LastRespawn = time.Now().UTC()
	if err := saveBeadRespawnState(townRoot, state); err != nil {
		return 0, err
	}
	return rec.Count, nil
}

// ResetBeadRespawnCount resets the respawn counter for beadID to zero.
// Used by `gt sling respawn-reset` to allow re-dispatch after investigation.
func ResetBeadRespawnCount(workDir, beadID string) error {
	respawnMu.Lock()
	defer respawnMu.Unlock()

	townRoot, err := workspace.Find(workDir)
	if err != nil || townRoot == "" {
		townRoot = workDir
	}

	unlock, err := lockBeadRespawnState(townRoot)
	if err != nil {
		return err
	}
	defer unlock()
	state, err := loadBeadRespawnState(townRoot)
	if err != nil {
		return err
	}
	delete(state.Beads, beadID)
	return saveBeadRespawnState(townRoot, state)
}
