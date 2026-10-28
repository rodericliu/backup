// Package schedule computes backup plans. It is shared by the agent, which
// executes the plan, and the server, which predicts it (PRD 6.3).
package schedule

import (
	"cmp"
	"slices"
	"time"

	"github.com/rodericliu/backup/internal/api"
)

const day = 24 * time.Hour

// Slot is one planned backup of a project.
type Slot struct {
	Project  string
	Priority int
	Type     api.JobType
	At       time.Time
}

// Compare orders slots by planned time, then full before incremental, then
// project priority (lower first), then project name (PRD S-3).
func Compare(a, b Slot) int {
	if c := a.At.Compare(b.At); c != 0 {
		return c
	}
	if a.Type != b.Type {
		if a.Type == api.JobFull {
			return -1
		}
		return 1
	}
	return cmp.Or(
		cmp.Compare(a.Priority, b.Priority),
		cmp.Compare(a.Project, b.Project),
	)
}

// ProjectSlots returns the planned slots of p in the half-open interval
// (from, to], in time order. Periods are aligned to midnight in loc: a full
// backup runs at every multiple of the full interval plus the offset, and an
// incremental at every multiple of the incremental interval in between (PRD
// S-2). Disabled projects have no slots.
func ProjectSlots(p *api.ProjectConfig, loc *time.Location, from, to time.Time) []Slot {
	full := p.FullInterval.Std()
	if !p.Enabled() || full <= 0 || !to.After(from) {
		return nil
	}
	step := full
	if p.HasIncr() {
		step = p.IncrInterval.Std()
	}
	off := p.Offset.Std()

	var (
		slots []Slot
		last  time.Time
	)
	// emit adds the slots of the period starting start after local midnight.
	// Times are computed on the wall clock so that schedules keep their local
	// hour across daylight saving changes; a wall time that does not exist
	// normalizes onto the next slot and is emitted only once.
	emit := func(y int, m time.Month, d int, start time.Duration) {
		for k := range int(full / step) {
			at := time.Date(y, m, d, 0, int((start+off+time.Duration(k)*step)/time.Minute), 0, 0, loc)
			if !at.After(last) {
				continue
			}
			last = at
			if !at.After(from) || at.After(to) {
				continue
			}
			typ := api.JobIncr
			if k == 0 {
				typ = api.JobFull
			}
			slots = append(slots, Slot{Project: p.Name, Priority: p.Priority, Type: typ, At: at})
		}
	}

	// A slot lies within [periodStart, periodStart+2*full) because the offset is
	// smaller than the full interval, so start scanning one period early.
	y, m, d := from.In(loc).Add(-full - off).Date()
	for midnight := time.Date(y, m, d, 0, 0, 0, 0, loc); !midnight.After(to); midnight = nextMidnight(midnight) {
		y, m, d := midnight.Date()
		if full <= day {
			for j := range int(day / full) {
				emit(y, m, d, time.Duration(j)*full)
			}
			continue
		}
		if civilDay(midnight)%int64(full/day) == 0 {
			emit(y, m, d, 0)
		}
	}
	return slots
}

// Slots returns the merged, ordered slots of all enabled projects in (from, to].
func Slots(projects []api.ProjectConfig, loc *time.Location, from, to time.Time) []Slot {
	var all []Slot
	for i := range projects {
		all = append(all, ProjectSlots(&projects[i], loc, from, to)...)
	}
	slices.SortStableFunc(all, Compare)
	return all
}

// nextMidnight returns the following local midnight, which is robust against
// days that are not 24h long due to daylight saving changes.
func nextMidnight(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d+1, 0, 0, 0, 0, t.Location())
}

// civilDay returns the number of days between 1970-01-01 and t's local date.
func civilDay(t time.Time) int64 {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Unix() / int64(day/time.Second)
}
