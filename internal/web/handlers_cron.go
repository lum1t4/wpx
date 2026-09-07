package web

import (
	"errors"
	"net/http"
	"strings"
	"sync"

	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
	"github.com/lum1t4/wpx/internal/store"
)

var cronSiteLocks = struct {
	sync.Mutex
	entries map[cronLockKey]*cronLock
}{entries: make(map[cronLockKey]*cronLock)}

type cronLockKey struct {
	server *Server
	siteID string
}
type cronLock struct {
	mutex      sync.Mutex
	references int
}

func lockCronSite(server *Server, siteID string) func() {
	key := cronLockKey{server, siteID}
	cronSiteLocks.Lock()
	entry := cronSiteLocks.entries[key]
	if entry == nil {
		entry = &cronLock{}
		cronSiteLocks.entries[key] = entry
	}
	entry.references++
	cronSiteLocks.Unlock()
	entry.mutex.Lock()
	return func() {
		entry.mutex.Unlock()
		cronSiteLocks.Lock()
		entry.references--
		if entry.references == 0 {
			delete(cronSiteLocks.entries, key)
		}
		cronSiteLocks.Unlock()
	}
}

func (s *Server) registerCronRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /sites/{id}/cron", s.requireSession(s.cronPage))
	mux.HandleFunc("POST /sites/{id}/cron", s.requireSession(s.createCronSchedule))
	mux.HandleFunc("POST /sites/{id}/cron/wordpress", s.requireSession(s.setWordPressCron))
	mux.HandleFunc("POST /sites/{id}/cron/{schedule}", s.requireSession(s.updateCronSchedule))
	mux.HandleFunc("POST /sites/{id}/cron/{schedule}/delete", s.requireSession(s.deleteCronSchedule))
}

func (s *Server) cronPage(w http.ResponseWriter, r *http.Request, user store.User) {
	site, ok := s.authorizedSite(w, r, user, rbac.ManageAllSites)
	if !ok {
		return
	}
	s.renderCronPage(w, r, user, site, http.StatusOK, "")
}

func (s *Server) renderCronPage(w http.ResponseWriter, r *http.Request, user store.User, site model.Site, status int, message string) {
	schedules, err := s.store.ListCronSchedules(r.Context(), site.ID)
	if err != nil {
		http.Error(w, "could not load cron schedules", http.StatusInternalServerError)
		return
	}
	setting, err := s.store.WordPressCronSetting(r.Context(), site.ID)
	if err != nil {
		http.Error(w, "could not load WordPress cron settings", http.StatusInternalServerError)
		return
	}
	data := pageData{Title: "Cron jobs", Section: "cron", User: &user, CSRF: s.ensureCSRF(w, r), Site: &site, CronSchedules: schedules, WordPressCronSetting: &setting, CanManageCron: true, Message: message}
	if status >= 400 {
		data.Form = map[string]string{}
		for _, key := range []string{"name", "executable", "arguments", "preset", "minute", "hour", "day_of_month", "month", "day_of_week", "replaced", "enabled"} {
			data.Form[key] = r.FormValue(key)
		}
	}
	if r.URL.Query().Get("saved") == "yes" {
		data.Message = "Cron settings saved and applied."
	}
	if r.URL.Query().Get("queued") == "yes" {
		data.Message = "Cron change queued. Follow its result in Activity."
	}
	s.renderStatus(w, "site_cron.html", status, data)
}

func cronFormSchedule(r *http.Request, siteID, id string) (model.CronSchedule, error) {
	expression, err := cronFormExpression(r)
	if err != nil {
		return model.CronSchedule{}, err
	}
	command := []string{strings.TrimSpace(r.FormValue("executable"))}
	for _, argument := range strings.Split(strings.ReplaceAll(r.FormValue("arguments"), "\r\n", "\n"), "\n") {
		if argument != "" {
			command = append(command, argument)
		}
	}
	schedule := model.CronSchedule{ID: id, SiteID: siteID, Name: strings.TrimSpace(r.FormValue("name")), Expression: expression, Command: command, Enabled: r.FormValue("enabled") == "yes", ApplyStatus: "pending"}
	if id != "" {
		return schedule, model.ValidateCronSchedule(schedule)
	}
	return schedule, nil
}

func cronFormExpression(r *http.Request) (string, error) {
	if expression := strings.TrimSpace(r.FormValue("schedule_expression")); expression != "" {
		if err := model.ValidateCronExpression(expression); err != nil {
			return "", err
		}
		return expression, nil
	}
	switch r.FormValue("preset") {
	case "hourly":
		return "0 * * * *", nil
	case "daily":
		return "0 3 * * *", nil
	case "weekly":
		return "0 3 * * 0", nil
	case "custom":
		expression := strings.Join([]string{r.FormValue("minute"), r.FormValue("hour"), r.FormValue("day_of_month"), r.FormValue("month"), r.FormValue("day_of_week")}, " ")
		if err := model.ValidateCronExpression(expression); err != nil {
			return "", err
		}
		return expression, nil
	default:
		return "", errors.New("choose a schedule preset or custom schedule")
	}
}

func (s *Server) createCronSchedule(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, ok := s.authorizedSite(w, r, user, rbac.ManageAllSites)
	if !ok {
		return
	}
	defer lockCronSite(s, site.ID)()
	schedule, err := cronFormSchedule(r, site.ID, "")
	if err == nil {
		schedule, err = s.store.CreateCronSchedule(r.Context(), user, schedule)
	}
	if err != nil {
		s.renderCronPage(w, r, user, site, http.StatusBadRequest, err.Error())
		return
	}
	s.respondCronQueued(w, r, user, site.ID, "Cron schedule")
}

func (s *Server) updateCronSchedule(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, ok := s.authorizedSite(w, r, user, rbac.ManageAllSites)
	if !ok {
		return
	}
	unlock := lockCronSite(s, site.ID)
	defer unlock()
	old, err := s.store.CronSchedule(r.Context(), site.ID, r.PathValue("schedule"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	schedule, err := cronFormSchedule(r, site.ID, old.ID)
	if err == nil {
		schedule.CreatedAt = old.CreatedAt
		err = s.store.UpdateCronSchedule(r.Context(), user, schedule)
	}
	if err != nil {
		s.renderCronPage(w, r, user, site, http.StatusBadRequest, err.Error())
		return
	}
	s.respondCronQueued(w, r, user, site.ID, "Cron schedule")
}

func (s *Server) deleteCronSchedule(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, ok := s.authorizedSite(w, r, user, rbac.ManageAllSites)
	if !ok {
		return
	}
	defer lockCronSite(s, site.ID)()
	_, err := s.store.RequestCronScheduleDelete(r.Context(), user, site.ID, r.PathValue("schedule"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.respondCronQueued(w, r, user, site.ID, "Cron deletion")
}

func (s *Server) setWordPressCron(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, ok := s.authorizedSite(w, r, user, rbac.ManageAllSites)
	if !ok {
		return
	}
	defer lockCronSite(s, site.ID)()
	if site.Kind != model.WordPress {
		http.NotFound(w, r)
		return
	}
	expression, err := cronFormExpression(r)
	setting := model.WordPressCronSetting{SiteID: site.ID, Replaced: r.FormValue("replaced") == "yes", Expression: expression, ApplyStatus: "pending"}
	if err == nil {
		err = s.store.SetWordPressCronSetting(r.Context(), user, setting)
	}
	if err != nil {
		s.renderCronPage(w, r, user, site, http.StatusBadRequest, err.Error())
		return
	}
	s.respondCronQueued(w, r, user, site.ID, "WordPress cron")
}

func (s *Server) respondCronQueued(w http.ResponseWriter, r *http.Request, user store.User, siteID, label string) {
	jobID, err := s.store.PendingCronJob(r.Context(), siteID)
	if err != nil {
		http.Error(w, "queued cron job could not be loaded", http.StatusInternalServerError)
		return
	}
	returnURL := "/sites/" + siteID + "/cron"
	if r.Header.Get("X-WPX-Action") != "partial" {
		returnURL += "?queued=yes"
	}
	s.respondQueuedAction(w, r, user, jobID, returnURL, label)
}
