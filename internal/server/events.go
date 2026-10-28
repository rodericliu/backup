package server

import (
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"sync"
	"time"
	"uuid"

	"github.com/rodericliu/backup/internal/api"
	"github.com/rodericliu/backup/internal/store"
)

// Server-sent event types of GET /api/v1/events/stream (PRD D-5).
const (
	// EventHost carries a HostView after every heartbeat.
	EventHost = "host"
	// EventJob carries a JobUpdate when a job is reported.
	EventJob = "job"
)

// JobUpdate is the data of an EventJob event.
type JobUpdate struct {
	Job store.Job `json:"job"`
	// Progress is the newest progress sample of a running job.
	Progress *api.Progress `json:"progress,omitzero"`
}

// sseKeepAlive is how often a comment is sent to keep connections open.
const sseKeepAlive = 15 * time.Second

// subscriberBuffer bounds the events queued for a slow client; further
// events are dropped for it.
const subscriberBuffer = 64

type sseMessage struct {
	typ  string
	data []byte
}

// broker fans server-sent events out to connected browsers.
type broker struct {
	mu   sync.Mutex
	subs map[chan sseMessage]struct{}
}

func (b *broker) subscribe() (<-chan sseMessage, func()) {
	ch := make(chan sseMessage, subscriberBuffer)
	b.mu.Lock()
	if b.subs == nil {
		b.subs = make(map[chan sseMessage]struct{})
	}
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}

// active reports whether any browser is subscribed.
func (b *broker) active() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs) > 0
}

// publishJob sends the current state of a job, with an optional progress
// sample, to subscribed browsers.
func (s *Server) publishJob(r *http.Request, id uuid.UUID, progress *api.Progress) {
	if !s.events.active() {
		return
	}
	j, err := s.store.Job(r.Context(), id)
	if err != nil {
		s.log.Warn("load job for event", "job", id.String(), "err", err)
		return
	}
	j.LogTail = ""
	s.publish(EventJob, JobUpdate{Job: *j, Progress: progress})
}

// publish sends an event to every subscriber without blocking.
func (s *Server) publish(typ string, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		s.log.Error("encode event", "type", typ, "err", err)
		return
	}
	m := sseMessage{typ, data}
	s.events.mu.Lock()
	defer s.events.mu.Unlock()
	for ch := range s.events.subs {
		select {
		case ch <- m:
		default:
		}
	}
}

// eventStream streams host and job updates to the browser.
func (s *Server) eventStream(w http.ResponseWriter, r *http.Request) error {
	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if _, err := fmt.Fprint(w, "retry: 3000\n\n"); err != nil {
		return nil
	}
	if err := rc.Flush(); err != nil {
		return nil
	}
	ch, cancel := s.events.subscribe()
	defer cancel()
	t := time.NewTicker(sseKeepAlive)
	defer t.Stop()
	for {
		select {
		case <-r.Context().Done():
			return nil
		case <-t.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return nil
			}
		case m := <-ch:
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", m.typ, m.data); err != nil {
				return nil
			}
		}
		if err := rc.Flush(); err != nil {
			return nil
		}
	}
}
