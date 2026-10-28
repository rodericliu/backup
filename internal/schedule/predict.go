package schedule

import (
	"slices"
	"time"

	"github.com/rodericliu/backup/internal/api"
)

// MaxHourLoad is the planned work per hour above which a host is considered
// overloaded (PRD S-11).
const MaxHourLoad = time.Hour

// Estimator returns the expected duration of a backup.
type Estimator func(project string, typ api.JobType) time.Duration

// Run is the predicted execution of a slot on a host's serial worker.
type Run struct {
	Slot
	Start time.Time
	End   time.Time
	// Superseded is set when a newer slot of the same project and type became
	// due before this one started (PRD S-4); Start and End are then zero.
	Superseded bool
}

// Predict simulates the host's single worker executing every slot of projects
// in (from, to], using est for job durations (PRD S-10). It applies the same
// ordering and supersede rules as the agent.
func Predict(projects []api.ProjectConfig, loc *time.Location, from, to time.Time, est Estimator) []Run {
	slots := Slots(projects, loc, from, to)
	runs := make([]Run, len(slots))
	for i, s := range slots {
		runs[i].Slot = s
	}

	type key struct {
		project string
		typ     api.JobType
	}
	var (
		queue   []int // indexes into runs, kept sorted by Compare
		pending = make(map[key]int)
		next    int // next slot to enqueue
		free    = from
	)
	enqueue := func(until time.Time) {
		for ; next < len(runs) && !runs[next].At.After(until); next++ {
			k := key{runs[next].Project, runs[next].Type}
			if old, ok := pending[k]; ok {
				runs[old].Superseded = true
				queue = slices.DeleteFunc(queue, func(i int) bool { return i == old })
			}
			pending[k] = next
			queue = append(queue, next)
		}
	}

	for next < len(runs) || len(queue) > 0 {
		enqueue(free)
		if len(queue) == 0 {
			free = runs[next].At
			continue
		}
		i := queue[0]
		queue = queue[1:]
		r := &runs[i]
		delete(pending, key{r.Project, r.Type})
		r.Start = free
		r.End = free.Add(est(r.Project, r.Type))
		free = r.End
	}
	return runs
}

// HourLoad is the total predicted work of the slots planned within one hour.
type HourLoad struct {
	Hour       time.Time
	Total      time.Duration
	Overloaded bool
}

// HourLoads groups non-superseded runs by the hour of their planned time and
// flags hours whose total exceeds MaxHourLoad (PRD S-11).
func HourLoads(runs []Run) []HourLoad {
	var loads []HourLoad
	for _, r := range runs {
		if r.Superseded {
			continue
		}
		h := r.At.Truncate(time.Hour)
		if n := len(loads); n == 0 || !loads[n-1].Hour.Equal(h) {
			loads = append(loads, HourLoad{Hour: h})
		}
		l := &loads[len(loads)-1]
		l.Total += r.End.Sub(r.Start)
		l.Overloaded = l.Total > MaxHourLoad
	}
	return loads
}
