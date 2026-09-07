package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/lum1t4/wpx/internal/store"
)

const (
	quickActionWait = 900 * time.Millisecond
	quickActionTick = 30 * time.Millisecond
)

type actionResponse struct {
	JobID       string `json:"job_id"`
	Status      string `json:"status"`
	Phase       string `json:"phase,omitempty"`
	Progress    int    `json:"progress,omitempty"`
	Message     string `json:"message"`
	RedirectURL string `json:"redirect_url"`
	PollURL     string `json:"poll_url"`
	ActivityURL string `json:"activity_url"`
}

func (s *Server) registerActionsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /actions/jobs/{id}", s.requireSession(s.actionJob))
}

// respondQueuedAction preserves ordinary POST/redirect/GET behavior and gives
// the optional browser enhancement a bounded chance to observe real worker
// completion. A timeout is an ongoing job, never an inferred success.
func (s *Server) respondQueuedAction(w http.ResponseWriter, r *http.Request, user store.User, jobID, redirectURL, label string) {
	if r.Header.Get("X-WPX-Action") != "partial" {
		http.Redirect(w, r, redirectURL, http.StatusSeeOther)
		return
	}
	deadline := time.NewTimer(quickActionWait)
	defer deadline.Stop()
	ticker := time.NewTicker(quickActionTick)
	defer ticker.Stop()
	for {
		job, err := s.store.JobForUser(r.Context(), user, jobID)
		if err != nil {
			http.Error(w, "action status is unavailable", http.StatusInternalServerError)
			return
		}
		if job.Status == "succeeded" || job.Status == "failed" {
			s.writeActionResponse(w, job, redirectURL, label)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-deadline.C:
			s.writeActionResponse(w, job, redirectURL, label)
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) actionJob(w http.ResponseWriter, r *http.Request, user store.User) {
	job, err := s.store.JobForUser(r.Context(), user, r.PathValue("id"))
	if err != nil {
		if store.IsJobNotFound(err) {
			http.NotFound(w, r)
		} else {
			http.Error(w, "action status is unavailable", http.StatusInternalServerError)
		}
		return
	}
	redirectURL := r.URL.Query().Get("return")
	if !safeLocalReturnURL(redirectURL) {
		redirectURL = "/jobs"
	}
	s.writeActionResponse(w, job, redirectURL, r.URL.Query().Get("label"))
}

func (s *Server) writeActionResponse(w http.ResponseWriter, job store.Job, redirectURL, label string) {
	label = strings.TrimSpace(label)
	if label == "" {
		label = "Action"
	}
	message := label + " is still running."
	status := http.StatusAccepted
	switch job.Status {
	case "succeeded":
		message, status = label+" completed.", http.StatusOK
	case "failed":
		message, status = job.Error, http.StatusUnprocessableEntity
		if strings.TrimSpace(message) == "" {
			message = label + " failed."
		}
	}
	poll := "/actions/jobs/" + url.PathEscape(job.ID) + "?return=" + url.QueryEscape(redirectURL) + "&label=" + url.QueryEscape(label)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(actionResponse{JobID: job.ID, Status: job.Status, Phase: job.Phase, Progress: job.Progress, Message: message, RedirectURL: redirectURL, PollURL: poll, ActivityURL: "/jobs"})
}

func safeLocalReturnURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "//") &&
		!strings.Contains(parsed.Path, `\`) && parsed.Host == "" && parsed.Scheme == "" && parsed.User == nil && !parsed.IsAbs()
}
