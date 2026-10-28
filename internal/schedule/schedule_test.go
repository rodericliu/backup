package schedule

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rodericliu/backup/internal/api"
)

func project(name string, tier api.Tier, offset time.Duration, prio int) api.ProjectConfig {
	p := api.Template(tier)
	p.Name = name
	p.Container = name + "_slave"
	p.Offset = api.Duration(offset)
	p.Priority = prio
	p.MySQL.User = "backup"
	return p
}

// esh01 is the default schedule of backup-esh-01 (PRD 6.3.2).
func esh01() []api.ProjectConfig {
	return []api.ProjectConfig{
		project("fetisch", api.TierImportant, 0, 1),
		project("poppen", api.TierImportant, time.Hour, 2),
		project("kmv2_maindb", api.TierImportant, 2*time.Hour, 3),
		project("gays", api.TierImportant, 3*time.Hour, 4),
		project("kmv2_message", api.TierNormal, 3*time.Hour, 5),
	}
}

func format(slots []Slot) []string {
	out := make([]string, len(slots))
	for i, s := range slots {
		out[i] = fmt.Sprintf("%s %s %s", s.At.Format("15:04"), s.Type, s.Project)
	}
	return out
}

var day0 = time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)

func TestSlotsESH01(t *testing.T) {
	got := format(Slots(esh01(), time.UTC, day0.Add(-time.Second), day0.Add(4*time.Hour-time.Second)))
	want := []string{
		"00:00 full fetisch",
		"00:00 incr poppen",
		"00:00 incr kmv2_maindb",
		"00:00 incr gays",
		"01:00 full poppen",
		"01:00 incr fetisch",
		"01:00 incr kmv2_maindb",
		"01:00 incr gays",
		"02:00 full kmv2_maindb",
		"02:00 incr fetisch",
		"02:00 incr poppen",
		"02:00 incr gays",
		"03:00 full gays",
		"03:00 full kmv2_message",
		"03:00 incr fetisch",
		"03:00 incr poppen",
		"03:00 incr kmv2_maindb",
	}
	if !slices.Equal(got, want) {
		t.Errorf("slots:\n got %s\nwant %s", strings.Join(got, "\n     "), strings.Join(want, "\n     "))
	}
}

func TestSlotsFullEveryCycle(t *testing.T) {
	for _, p := range esh01() {
		slots := ProjectSlots(&p, time.UTC, day0.Add(-time.Second), day0.Add(day-time.Second))
		var fulls, incrs int
		for _, s := range slots {
			if s.Type == api.JobFull {
				fulls++
				if got := s.At.Sub(day0) % p.FullInterval.Std(); got != p.Offset.Std() {
					t.Errorf("%s full at %s: not aligned to offset %s", p.Name, s.At, p.Offset)
				}
			} else {
				incrs++
			}
		}
		wantFulls := int(day / p.FullInterval.Std())
		wantIncrs := 0
		if p.HasIncr() {
			wantIncrs = int(day/p.IncrInterval.Std()) - wantFulls
		}
		if fulls != wantFulls || incrs != wantIncrs {
			t.Errorf("%s: %d full / %d incr per day, want %d / %d", p.Name, fulls, incrs, wantFulls, wantIncrs)
		}
	}
}

func TestProjectSlotsInterval(t *testing.T) {
	p := project("gays", api.TierImportant, 3*time.Hour, 0)
	tests := []struct {
		name     string
		from, to time.Time
		want     []string
	}{
		{"exclusive start", day0, day0.Add(time.Hour), []string{"01:00 incr gays"}},
		{"inclusive end", day0.Add(30 * time.Minute), day0.Add(3 * time.Hour), []string{"01:00 incr gays", "02:00 incr gays", "03:00 full gays"}},
		{"empty", day0.Add(time.Minute), day0.Add(59 * time.Minute), nil},
		{"reversed", day0.Add(time.Hour), day0, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := format(ProjectSlots(&p, time.UTC, tt.from, tt.to)); !slices.Equal(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestProjectSlotsDisabled(t *testing.T) {
	p := project("gays", api.TierImportant, 0, 0)
	p.Disabled = true
	if got := ProjectSlots(&p, time.UTC, day0, day0.Add(day)); len(got) != 0 {
		t.Errorf("disabled project has %d slots", len(got))
	}
}

func TestProjectSlotsTimezone(t *testing.T) {
	loc := time.FixedZone("CET", 3600)
	p := project("x", api.TierNormal, 3*time.Hour, 0)
	got := ProjectSlots(&p, loc, day0, day0.Add(day))
	if len(got) != 1 {
		t.Fatalf("got %d slots, want 1", len(got))
	}
	if h := got[0].At.In(loc).Hour(); h != 3 {
		t.Errorf("full at local hour %d, want 3", h)
	}
}

func TestProjectSlotsMultiDay(t *testing.T) {
	p := project("x", api.TierNormal, 0, 0)
	p.FullInterval = api.Duration(7 * day)
	got := ProjectSlots(&p, time.UTC, day0, day0.Add(28*day))
	if len(got) != 4 {
		t.Fatalf("got %d slots in 28 days, want 4", len(got))
	}
	for i := 1; i < len(got); i++ {
		if d := got[i].At.Sub(got[i-1].At); d != 7*day {
			t.Errorf("gap %s, want 168h", d)
		}
	}
	if got[0].At.Weekday() != time.Thursday { // 1970-01-01 was a Thursday
		t.Errorf("weekly full on %s, want Thursday", got[0].At.Weekday())
	}
}

func TestProjectSlotsDST(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip("tzdata unavailable:", err)
	}
	p := project("x", api.TierNormal, 3*time.Hour, 0)
	// 2026-10-25 is 25h long in Berlin.
	from := time.Date(2026, 10, 24, 12, 0, 0, 0, loc)
	got := ProjectSlots(&p, loc, from, from.Add(3*day))
	for _, s := range got {
		if h := s.At.In(loc).Hour(); h != 3 {
			t.Errorf("full at %s, want 03:00 local", s.At.In(loc))
		}
	}
	if len(got) != 3 {
		t.Errorf("got %d slots, want 3", len(got))
	}
}

func TestProjectSlotsSpringForward(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip("tzdata unavailable:", err)
	}
	p := project("x", api.TierImportant, 2*time.Hour, 0)
	// 02:00 does not exist on 2026-03-29 in Berlin.
	from := time.Date(2026, 3, 29, 0, 30, 0, 0, loc)
	got := ProjectSlots(&p, loc, from, from.Add(4*time.Hour))
	seen := make(map[time.Time]bool)
	for _, s := range got {
		if seen[s.At] {
			t.Errorf("duplicate slot at %s", s.At.In(loc))
		}
		seen[s.At] = true
	}
	if len(got) == 0 || got[0].Type != api.JobIncr || got[0].At.In(loc).Hour() != 1 {
		t.Errorf("first slot = %+v, want 01:00 incr", got)
	}
}
