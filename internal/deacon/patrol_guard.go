package deacon

import (
	"errors"
	"fmt"
	"os"

	"github.com/steveyegge/gastown/internal/estop"
)

// ErrPatrolHeld means starting work is forbidden, including when the hold state
// cannot be read. Callers must not turn this error into an alternate execution path.
var ErrPatrolHeld = errors.New("patrol held")

// CheckPatrolAllowed is the shared boundary for agent startup, dispatch and
// maintenance. It reads current state on every call; resuming does not require
// restarting the daemon or registering its timers again.
func CheckPatrolAllowed(townRoot string) error {
	if townRoot == "" {
		return fmt.Errorf("%w: town root is missing", ErrPatrolHeld)
	}
	if info, err := os.Stat(townRoot); err != nil {
		return fmt.Errorf("%w: cannot read town root: %v", ErrPatrolHeld, err)
	} else if !info.IsDir() {
		return fmt.Errorf("%w: town root is not a directory", ErrPatrolHeld)
	}
	if _, err := os.Lstat(estop.FilePath(townRoot)); err == nil {
		return fmt.Errorf("%w: E-STOP is active (use gt thaw after investigation)", ErrPatrolHeld)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("%w: cannot read E-STOP state: %v", ErrPatrolHeld, err)
	}
	paused, state, err := IsPaused(townRoot)
	if err != nil {
		return fmt.Errorf("%w: cannot read Deacon pause state: %v", ErrPatrolHeld, err)
	}
	if paused {
		return fmt.Errorf("%w: Deacon is paused by %s: %s (use gt deacon resume after investigation)", ErrPatrolHeld, state.PausedBy, state.Reason)
	}
	return nil
}
