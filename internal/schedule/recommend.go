package schedule

import (
	"cmp"
	"slices"
	"time"

	"github.com/rodericliu/backup/internal/api"
)

// OffsetStep is the granularity of recommended offsets.
const OffsetStep = 15 * time.Minute

// OffsetScore is the predicted load of a host when a project uses Offset.
type OffsetScore struct {
	Offset api.Duration `json:"offset"`
	// OverloadedHours counts hours whose planned work exceeds MaxHourLoad.
	OverloadedHours int `json:"overloaded_hours"`
	// MaxHourLoad is the largest planned work of an hour.
	MaxHourLoad api.Duration `json:"max_hour_load"`
	// TotalWait is the sum over all runs of the delay between the planned
	// and the predicted start, which grows when backups collide.
	TotalWait api.Duration `json:"total_wait"`
}

func compareScores(a, b OffsetScore) int {
	return cmp.Or(
		cmp.Compare(a.OverloadedHours, b.OverloadedHours),
		cmp.Compare(a.TotalWait, b.TotalWait),
		cmp.Compare(a.MaxHourLoad, b.MaxHourLoad),
		cmp.Compare(a.Offset, b.Offset),
	)
}

// RecommendOffset predicts the host's load for every offset of p in steps of
// OffsetStep, together with the other projects of the host, and returns the
// best offset and all candidates in offset order (PRD P-5). The best offset
// has the fewest overloaded hours, then the least waiting, then the lowest
// peak hour. p replaces any project of the same name in others.
func RecommendOffset(others []api.ProjectConfig, p api.ProjectConfig, loc *time.Location, now time.Time, est Estimator) (api.Duration, []OffsetScore) {
	full := p.FullInterval.Std()
	if full <= 0 {
		return 0, nil
	}
	projects := slices.DeleteFunc(slices.Clone(others), func(o api.ProjectConfig) bool { return o.Name == p.Name || !o.Enabled() })
	horizon := 24 * time.Hour
	for _, o := range append(projects, p) {
		horizon = max(horizon, 2*o.FullInterval.Std())
	}
	// Start at the next local midnight so that every candidate sees whole periods.
	y, m, d := now.In(loc).Date()
	from := time.Date(y, m, d+1, 0, 0, 0, 0, loc)
	to := from.Add(horizon)

	p.Disabled = false
	idx := len(projects)
	projects = append(projects, p)
	step := min(OffsetStep, full)
	var scores []OffsetScore
	for off := time.Duration(0); off < full; off += step {
		projects[idx].Offset = api.Duration(off)
		runs := Predict(projects, loc, from, to, est)
		s := OffsetScore{Offset: api.Duration(off)}
		for _, r := range runs {
			if !r.Superseded {
				s.TotalWait += api.Duration(r.Start.Sub(r.At))
			}
		}
		for _, l := range HourLoads(runs) {
			s.MaxHourLoad = max(s.MaxHourLoad, api.Duration(l.Total))
			if l.Overloaded {
				s.OverloadedHours++
			}
		}
		scores = append(scores, s)
	}
	return slices.MinFunc(scores, compareScores).Offset, scores
}
