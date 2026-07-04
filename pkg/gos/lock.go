package gos

import "sync/atomic"

var (
	// LockedCount tracks the number of currently active FOR UPDATE operations.
	LockedCount = atomic.Int64{}
	// LockedActive enables dynamic lock timeout calculation when true.
	LockedActive = atomic.Bool{}
)

// LockTimeout returns the Oracle FOR UPDATE WAIT timeout in seconds.
// When LockedActive is false the default of 10 seconds is used.
func LockTimeout() int64 {
	if LockedActive.Load() {
		return dynamicLockTime()
	}
	return 10
}

// dynamicLockTime derives a lock timeout from the current concurrent lock count,
// clamped to the range [1, 15].
func dynamicLockTime() int64 {
	count := LockedCount.Load()
	t := 16 - count
	switch {
	case t < 1:
		return 1
	case t > 15:
		return 15
	default:
		return t
	}
}
