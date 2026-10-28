// Package server implements backup-server: the web and management API, the
// agent API and background maintenance (PRD 5.1, 8).
package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rodericliu/backup/internal/crypto"
	"github.com/rodericliu/backup/internal/server/config"
	"github.com/rodericliu/backup/internal/store"
	"github.com/rodericliu/backup/internal/version"
)

// API path prefixes (PRD 8).
const (
	AgentAPIPrefix = "/api/agent/v1"
	WebAPIPrefix   = "/api/v1"
)

// Options configures a Server.
type Options struct {
	Store  *store.Store
	Sealer *crypto.Sealer
	Config *config.Config
	Logger *slog.Logger
	// Now returns the current time; default time.Now.
	Now func() time.Time
}

// Server is the backup-server application.
type Server struct {
	store  *store.Store
	sealer *crypto.Sealer
	cfg    *config.Config
	log    *slog.Logger
	now    func() time.Time

	web   *http.ServeMux
	agent *http.ServeMux
	// tasks run every minute in the background.
	tasks   []task
	limiter loginLimiter
}

type task struct {
	name string
	run  func(context.Context) error
}

// New returns a server with all routes registered.
func New(opts Options) *Server {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	s := &Server{
		store:  opts.Store,
		sealer: opts.Sealer,
		cfg:    opts.Config,
		log:    opts.Logger,
		now:    opts.Now,
		web:    http.NewServeMux(),
		agent:  http.NewServeMux(),
	}
	s.routes()
	return s
}

func (s *Server) routes() {
	s.web.Handle("GET /healthz", s.handle(s.healthz))
	s.agent.Handle("GET /healthz", s.handle(s.healthz))

	s.web.Handle("POST "+WebAPIPrefix+"/auth/login", sameOrigin(s.handle(s.login)))
	s.webAPI("POST /auth/logout", s.logout)
	s.webAPI("GET /auth/me", s.me)
	s.webAPI("POST /auth/password", s.changePassword)

	s.webAPI("GET /users", s.listUsers)
	s.webAPI("POST /users", s.createUser)
	s.webAPI("PATCH /users/{id}", s.updateUser)
	s.webAPI("POST /users/{id}/password", s.resetPassword)

	s.webAPI("GET /audit-logs", s.listAuditLogs)

	s.tasks = append(s.tasks, task{"delete expired sessions", s.deleteExpiredSessions})
}

// webAPI registers an authenticated web API route; pattern is
// "METHOD /path" relative to WebAPIPrefix.
func (s *Server) webAPI(pattern string, h handlerFunc) {
	method, path, _ := strings.Cut(pattern, " ")
	s.web.Handle(method+" "+WebAPIPrefix+path, sameOrigin(s.requireUser(s.handle(h))))
}

// WebHandler serves the web UI and the management API.
func (s *Server) WebHandler() http.Handler { return s.accessLog(securityHeaders(s.web)) }

// AgentHandler serves the agent API.
func (s *Server) AgentHandler() http.Handler { return s.accessLog(s.agent) }

// Handler serves both APIs on one listener, used when agent_listen is empty.
func (s *Server) Handler() http.Handler {
	web, agent := s.WebHandler(), s.AgentHandler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == AgentAPIPrefix || strings.HasPrefix(r.URL.Path, AgentAPIPrefix+"/") {
			agent.ServeHTTP(w, r)
			return
		}
		web.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Strict-Transport-Security", "max-age=31536000")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

type health struct {
	Status  string `json:"status"`
	Version string `json:"version"`
	Error   string `json:"error,omitzero"`
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) error {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		s.log.Warn("health check: database unavailable", "err", err)
		return writeJSON(w, http.StatusServiceUnavailable, health{Status: "unavailable", Version: version.Version, Error: "database unavailable"})
	}
	return writeJSON(w, http.StatusOK, health{Status: "ok", Version: version.Version})
}

// Run serves HTTPS on the configured listeners and runs background
// maintenance until ctx is done, then shuts down gracefully.
func (s *Server) Run(ctx context.Context, tlsConfig *tls.Config) error {
	type listener struct {
		addr    string
		handler http.Handler
	}
	listeners := []listener{{s.cfg.Listen, s.WebHandler()}}
	if s.cfg.AgentListen != "" {
		listeners = append(listeners, listener{s.cfg.AgentListen, s.AgentHandler()})
	} else {
		listeners[0].handler = s.Handler()
	}

	var servers []*http.Server
	errc := make(chan error, len(listeners))
	for _, l := range listeners {
		ln, err := net.Listen("tcp", l.addr)
		if err != nil {
			for _, srv := range servers {
				srv.Close()
			}
			return fmt.Errorf("listen %s: %w", l.addr, err)
		}
		srv := &http.Server{
			Handler:           l.handler,
			TLSConfig:         tlsConfig,
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       2 * time.Minute,
			ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelWarn),
			BaseContext:       func(net.Listener) context.Context { return ctx },
		}
		servers = append(servers, srv)
		s.log.Info("listening", "addr", ln.Addr().String())
		go func() {
			if err := srv.ServeTLS(ln, "", ""); !errors.Is(err, http.ErrServerClosed) {
				errc <- err
			}
		}()
	}

	var wg sync.WaitGroup
	wg.Go(func() { s.maintain(ctx) })

	var err error
	select {
	case <-ctx.Done():
	case err = <-errc:
	}
	shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	for _, srv := range servers {
		if e := srv.Shutdown(shutdown); e != nil {
			err = errors.Join(err, e)
		}
	}
	wg.Wait()
	return err
}

// maintain runs the background tasks every minute until ctx is done.
func (s *Server) maintain(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		s.runTasks(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Server) runTasks(ctx context.Context) {
	for _, t := range s.tasks {
		if ctx.Err() != nil {
			return
		}
		if err := t.run(ctx); err != nil {
			s.log.Error("background task", "task", t.name, "err", err)
		}
	}
}

// LoadTLS loads the server certificate and returns a TLS configuration.
func LoadTLS(certFile, keyFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load server certificate: %w", err)
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}, nil
}
