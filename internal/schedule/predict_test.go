package schedule

import (
	"testing"
	"time"

	"github.com/rodericliu/backup/internal/api"
)

func fixed(full, incr time.Duration) Estimator {
	return func(_ string, typ api.JobType) time.Duration {
		if typ == api.JobFull {
			return full
		}
		return incr
	}
}

func TestPredictSerial(t *testing.T) {
	runs := Predict(esh01(), time.UTC, day0.Add(-time.Second), day0.Add(day), fixed(20*time.Minute, 5*time.Minute))
	if len(runs) == 0 {
		t.Fatal("no runs")
	}
	for i, r := range runs {
		if r.Superseded {
			t.Errorf("%s %s at %s superseded unexpectedly", r.Type, r.Project, r.At)
			continue
		}
		if r.Start.Before(r.At) {
			t.Errorf("%s %s starts %s before planned %s", r.Type, r.Project, r.Start, r.At)
		}
		if i > 0 && r.Start.Before(runs[i-1].End) {
			t.Errorf("%s %s overlaps previous run", r.Type, r.Project)
		}
	}
	// Hour 0: full 20m + 3 incr × 5m = 35m.
	if got := runs[3].End.Sub(day0); got != 35*time.Minute {
		t.Errorf("hour 0 finishes after %s, want 35m", got)
	}
	for _, l := range HourLoads(runs) {
		if l.Overloaded {
			t.Errorf("hour %s overloaded with %s", l.Hour, l.Total)
		}
	}
}

func TestPredictSupersede(t *testing.T) {
	p := project("slow", api.TierImportant, 0, 1)
	q := project("fast", api.TierImportant, 0, 2)
	est := func(project string, typ api.JobType) time.Duration {
		if project == "slow" && typ == api.JobFull {
			return 150 * time.Minute
		}
		return time.Minute
	}
	runs := Predict([]api.ProjectConfig{p, q}, time.UTC, day0.Add(-time.Second), day0.Add(3*time.Hour), est)

	var superseded []string
	for _, r := range runs {
		if r.Superseded {
			superseded = append(superseded, r.At.Format("15:04")+" "+string(r.Type)+" "+r.Project)
		}
	}
	// The slow full blocks the worker until 02:30, so fast's 01:00 incr is
	// replaced by its 02:00 incr, and slow's 01:00 incr by its 02:00 incr.
	want := map[string]bool{"01:00 incr slow": true, "01:00 incr fast": true}
	if len(superseded) != len(want) {
		t.Fatalf("superseded = %v, want %v", superseded, want)
	}
	for _, s := range superseded {
		if !want[s] {
			t.Errorf("unexpected superseded %q", s)
		}
	}
}

func TestHourLoadsOverloaded(t *testing.T) {
	runs := Predict(esh01(), time.UTC, day0.Add(-time.Second), day0.Add(time.Hour-time.Second), fixed(50*time.Minute, 5*time.Minute))
	loads := HourLoads(runs)
	if len(loads) != 1 {
		t.Fatalf("got %d hour loads, want 1", len(loads))
	}
	if l := loads[0]; l.Total != 65*time.Minute || !l.Overloaded {
		t.Errorf("load = %s overloaded=%v, want 65m overloaded", l.Total, l.Overloaded)
	}
}
