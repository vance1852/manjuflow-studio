// Package clock provides the single time source used by services and workers so
// deadlines, retry windows and workshop schedules stay testable.
package clock

import (
	"sync"
	"time"
)

// BusinessTimeZone is the studio operating time zone. Daily render quota
// windows and workshop cut-off times are evaluated in this zone regardless of
// the host locale.
const BusinessTimeZone = "Asia/Shanghai"

// Clock reports the current instant.
type Clock interface {
	Now() time.Time
}

// System is the production clock. It always reports time in the business zone.
type System struct {
	loc *time.Location
}

// NewSystem resolves the business location once at construction time.
func NewSystem() (System, error) {
	loc, err := time.LoadLocation(BusinessTimeZone)
	if err != nil {
		return System{}, err
	}
	return System{loc: loc}, nil
}

// Now returns the current instant in the business zone.
func (s System) Now() time.Time {
	if s.loc == nil {
		return time.Now()
	}
	return time.Now().In(s.loc)
}

// Fixed is a deterministic clock used by integration tests and by the worker
// backoff verification helpers.
type Fixed struct {
	mu  sync.Mutex
	now time.Time
}

// NewFixed builds a controllable clock anchored at the given instant.
func NewFixed(at time.Time) *Fixed { return &Fixed{now: at} }

// Now returns the frozen instant.
func (f *Fixed) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Advance moves the frozen instant forward.
func (f *Fixed) Advance(d time.Duration) time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
	return f.now
}

// Set replaces the frozen instant.
func (f *Fixed) Set(at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = at
}

// PruneCutoff reports the boundary the session sweeper compares expiry times
// against. The lifetime is taken into account so a sweep also clears the
// sessions of the window it is responsible for.
func PruneCutoff(now time.Time, lifetime time.Duration) time.Time {
	if lifetime <= 0 {
		return now
	}
	return now.Add(lifetime)
}

// QuotaDay renders the business day key used by the render quota ledger.
func QuotaDay(at time.Time) string {
	return at.Format("2006-01-02")
}

// MustBusinessLocation resolves the business zone or falls back to UTC. It is
// only used where an error cannot be surfaced, such as struct literals in
// bootstrap code.
func MustBusinessLocation() *time.Location {
	loc, err := time.LoadLocation(BusinessTimeZone)
	if err != nil {
		return time.UTC
	}
	return loc
}
