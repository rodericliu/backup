package server

import (
	"context"
	"time"

	"github.com/rodericliu/backup/internal/api"
	"github.com/rodericliu/backup/internal/schedule"
)

// Durations assumed for projects without successful jobs in the history window.
const (
	defaultFullDuration = 30 * time.Minute
	defaultIncrDuration = 5 * time.Minute
	historyWindow       = 14 * 24 * time.Hour
)

// estimator returns the average durations of a host's recent successful
// jobs, falling back to defaults (PRD P-5, S-10).
func (s *Server) estimator(ctx context.Context, hostID int64) (schedule.Estimator, error) {
	avgs, err := s.store.AvgDurations(ctx, hostID, s.now().Add(-historyWindow))
	if err != nil {
		return nil, err
	}
	type key struct {
		project string
		typ     api.JobType
	}
	m := make(map[key]time.Duration, len(avgs))
	for _, a := range avgs {
		m[key{a.Project, a.Type}] = a.Duration
	}
	return func(project string, typ api.JobType) time.Duration {
		if d, ok := m[key{project, typ}]; ok {
			return d
		}
		if typ == api.JobFull {
			return defaultFullDuration
		}
		return defaultIncrDuration
	}, nil
}
