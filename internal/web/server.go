// Package web is the unprivileged control plane. Handlers validate user intent,
// persist desired state, and call typed broker operations. They never invoke a
// package manager, service command, or privileged filesystem operation directly.
//
// Routes declare session and global capability requirements. Site handlers also
// verify the user's assignment and the capability required by each operation.
// Templates receive presentation flags, never authority: hiding a control does
// not replace authorization in the handler.
package web

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"time"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/config"
	"github.com/lum1t4/wpx/internal/store"
)

//go:embed templates/*.html static/*
var templateFiles embed.FS

type Server struct {
	cfg       config.Config
	store     *store.Store
	templates *template.Template
	logger    *slog.Logger
	broker    brokerCaller
}

type brokerCaller interface {
	Call(context.Context, broker.Operation, string, any, any) error
}

func New(cfg config.Config, state *store.Store, privileged brokerCaller, logger *slog.Logger) (*Server, error) {
	tmpl, err := template.New("wpx").Funcs(template.FuncMap{
		"bytes": humanBytes, "jobLabel": jobLabel,
		"assigned": userAssignedSite, "selectedSite": selectedSite,
	}).ParseFS(templateFiles, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse web templates: %w", err)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{cfg: cfg, store: state, templates: tmpl, logger: logger, broker: privileged}, nil
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	httpServer := &http.Server{
		Addr: s.cfg.ListenAddress, Handler: s.Handler(),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute,
		MaxHeaderBytes: 1 << 20,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	err := httpServer.ListenAndServeTLS(s.cfg.TLSCertPath, s.cfg.TLSKeyPath)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.Health(ctx); err != nil {
		http.Error(w, "state unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
}
