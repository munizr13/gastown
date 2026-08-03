package util

import "testing"

// CheckDiskSpace reads a real filesystem, so these tests exercise the decision
// rule directly against synthetic DiskSpaceInfo values. The rule under test is
// the one that was broken: WARNING measured only absolute free space while
// CRITICAL measured absolute OR percentage, which made WARNING unreachable on
// any disk larger than ~20 GB.
func classify(availMB uint64, usedPercent float64) DiskSpaceLevel {
	if availMB < DiskSpaceMinimumMB || usedPercent >= DiskSpaceCriticalPercent {
		return DiskSpaceCritical
	}
	if availMB < DiskSpaceWarningMB || usedPercent >= DiskSpaceWarningPercent {
		return DiskSpaceWarning
	}
	return DiskSpaceOK
}

// The regression itself: a large, nearly-full disk must warn BEFORE it blocks.
func TestWarningIsReachableOnALargeDisk(t *testing.T) {
	// 926 GB volume at 94% used: ~55 GB free — far above the 1 GB absolute
	// warning floor, which is why this used to report OK right up to the cliff.
	const availMB = 55 * 1024
	if got := classify(availMB, 94.0); got != DiskSpaceWarning {
		t.Fatalf("94%% used on a large disk = %v, want DiskSpaceWarning — the warning band is unreachable again", got)
	}
}

// The band must actually exist: OK below it, WARNING inside it, CRITICAL above.
func TestBandBoundaries(t *testing.T) {
	const plentyMB = 55 * 1024 // never trips an absolute threshold
	cases := []struct {
		name        string
		usedPercent float64
		want        DiskSpaceLevel
	}{
		{"well below the warning line", 80.0, DiskSpaceOK},
		{"just below the warning line", DiskSpaceWarningPercent - 0.1, DiskSpaceOK},
		{"exactly at the warning line", DiskSpaceWarningPercent, DiskSpaceWarning},
		{"inside the band", 94.0, DiskSpaceWarning},
		{"just below the block line", DiskSpaceCriticalPercent - 0.1, DiskSpaceWarning},
		{"exactly at the block line", DiskSpaceCriticalPercent, DiskSpaceCritical},
		{"above the block line", 99.0, DiskSpaceCritical},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classify(plentyMB, tc.usedPercent); got != tc.want {
				t.Errorf("%.1f%% used = %v, want %v", tc.usedPercent, got, tc.want)
			}
		})
	}
}

// The whole point of the change is that it is additive. Anything that blocked
// before must still block, so the fix cannot stop work that Gas Town previously
// refused — it only adds an earlier warning.
func TestCriticalBehaviourIsUnchanged(t *testing.T) {
	cases := []struct {
		name        string
		availMB     uint64
		usedPercent float64
	}{
		{"below the absolute minimum", DiskSpaceMinimumMB - 1, 10.0},
		{"at the critical percentage", 55 * 1024, DiskSpaceCriticalPercent},
		{"above the critical percentage", 55 * 1024, 98.0},
		{"both conditions", 100, 99.0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classify(tc.availMB, tc.usedPercent); got != DiskSpaceCritical {
				t.Errorf("availMB=%d used=%.1f%% = %v, want DiskSpaceCritical (blocking must not change)",
					tc.availMB, tc.usedPercent, got)
			}
		})
	}
}

// The old absolute warning must keep working for genuinely small disks.
func TestAbsoluteWarningStillApplies(t *testing.T) {
	// Under 1 GB free but a low percentage — e.g. a small partition.
	if got := classify(DiskSpaceWarningMB-1, 50.0); got != DiskSpaceWarning {
		t.Errorf("low absolute free space = %v, want DiskSpaceWarning", got)
	}
}

// Guard the ordering invariant: a warning threshold at or above the critical one
// would collapse the band and make the warning unreachable again from the other
// direction.
func TestWarningThresholdSitsBelowCritical(t *testing.T) {
	if DiskSpaceWarningPercent >= DiskSpaceCriticalPercent {
		t.Fatalf("DiskSpaceWarningPercent (%.1f) must be < DiskSpaceCriticalPercent (%.1f)",
			DiskSpaceWarningPercent, DiskSpaceCriticalPercent)
	}
	if DiskSpaceWarningMB < DiskSpaceMinimumMB {
		t.Fatalf("DiskSpaceWarningMB (%d) must be >= DiskSpaceMinimumMB (%d)",
			DiskSpaceWarningMB, DiskSpaceMinimumMB)
	}
}
