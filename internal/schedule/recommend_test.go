package schedule

import (
	"testing"
	"time"

	"github.com/rodericliu/backup/internal/api"
)

func TestRecommendOffset(t *testing.T) {
	all := esh01()
	others, gays := all[:3], all[3]
	gays.Offset = 0
	est := fixed(40*time.Minute, 5*time.Minute)
	best, scores := RecommendOffset(others, gays, time.UTC, day0.Add(10*time.Hour), est)
	if len(scores) != 16 {
		t.Fatalf("candidates = %d, want 16 (4h in 15m steps)", len(scores))
	}
	// Hours 0-2 already hold a 40m full backup; gays belongs in hour 3.
	if best < api.Duration(3*time.Hour) || best >= api.Duration(4*time.Hour) {
		t.Errorf("best offset = %s, want within [3h, 4h)", best)
	}
	byOffset := make(map[api.Duration]OffsetScore)
	for _, s := range scores {
		byOffset[s.Offset] = s
	}
	if s := byOffset[best]; s.OverloadedHours != 0 {
		t.Errorf("best score = %+v", s)
	}
	if s := byOffset[0]; s.OverloadedHours == 0 || s.MaxHourLoad.Std() <= MaxHourLoad {
		t.Errorf("offset 0 collides with fetisch but scores %+v", s)
	}
}

func TestRecommendOffsetReplacesSameName(t *testing.T) {
	all := esh01()
	// Re-recommending an existing project ignores its current offset.
	best, _ := RecommendOffset(all[:4], all[3], time.UTC, day0, fixed(40*time.Minute, 5*time.Minute))
	if best < api.Duration(3*time.Hour) || best >= api.Duration(4*time.Hour) {
		t.Errorf("best offset = %s", best)
	}
	daily := all[4]
	_, scores := RecommendOffset(all[:4], daily, time.UTC, day0, fixed(40*time.Minute, 5*time.Minute))
	if len(scores) != 96 {
		t.Errorf("daily candidates = %d, want 96", len(scores))
	}
}
