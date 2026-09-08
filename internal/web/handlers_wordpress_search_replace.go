package web

import (
	"context"
	"net/http"
	"time"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
	"github.com/lum1t4/wpx/internal/store"
)

const wordpressSearchReplaceFormBytes = 8 << 10

func (s *Server) registerWordPressSearchReplaceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /sites/{id}/wordpress/search-replace", s.requireSession(s.wordpressSearchReplacePage))
	mux.HandleFunc("POST /sites/{id}/wordpress/search-replace/preview", s.requireSession(s.previewWordPressSearchReplace))
	mux.HandleFunc("POST /sites/{id}/wordpress/search-replace/apply", s.requireSession(s.applyWordPressSearchReplace))
}

func (s *Server) wordpressSearchReplacePage(w http.ResponseWriter, r *http.Request, user store.User) {
	site, ok := s.wordpressSearchReplaceSite(w, r, user)
	if !ok {
		return
	}
	s.renderWordPressSearchReplace(w, r, user, site, http.StatusOK, "", nil, "")
}

func (s *Server) previewWordPressSearchReplace(w http.ResponseWriter, r *http.Request, user store.User) {
	if !parseWordPressSearchReplaceForm(w, r) {
		return
	}
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, ok := s.wordpressSearchReplaceSite(w, r, user)
	if !ok {
		return
	}
	change := wordpressSearchReplaceChange(r)
	if err := model.ValidateWordPressSearchReplace(change); err != nil {
		s.renderWordPressSearchReplace(w, r, user, site, http.StatusBadRequest, err.Error(), nil, "")
		return
	}
	targetID := r.FormValue("target_id")
	if err := s.store.ValidateWordPressSearchReplacePreviewContext(r.Context(), user, site.ID, targetID, change); err != nil {
		s.renderWordPressSearchReplace(w, r, user, site, http.StatusConflict, "The site or recovery storage is not ready. Check Activity and try again.", nil, "")
		return
	}
	if s.broker == nil {
		s.renderWordPressSearchReplace(w, r, user, site, http.StatusServiceUnavailable, "WordPress search and replace is unavailable.", nil, "")
		return
	}
	request := broker.WordPressSearchReplaceRequest{Site: site, Change: change, DryRun: true}
	var result broker.WordPressSearchReplaceResult
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := s.broker.Call(ctx, broker.OpWordPressSearchReplace, "wordpress.search_replace.preview:"+site.ID+":"+mustRandomHex(12), request, &result); err != nil {
		s.logger.Warn("preview WordPress search and replace failed", "site", site.ID)
		s.renderWordPressSearchReplace(w, r, user, site, http.StatusBadGateway, "The preview could not be completed. No changes were made.", nil, "")
		return
	}
	previewToken, err := s.store.CreateWordPressSearchReplacePreview(r.Context(), user, site.ID, targetID, change, result)
	if err != nil {
		s.logger.Error("save WordPress search and replace preview failed", "site", site.ID)
		s.renderWordPressSearchReplace(w, r, user, site, http.StatusInternalServerError, "The preview could not be saved. No changes were made.", nil, "")
		return
	}
	s.renderWordPressSearchReplace(w, r, user, site, http.StatusOK, "", &result, previewToken)
}

func (s *Server) applyWordPressSearchReplace(w http.ResponseWriter, r *http.Request, user store.User) {
	if !parseWordPressSearchReplaceForm(w, r) {
		return
	}
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, ok := s.wordpressSearchReplaceSite(w, r, user)
	if !ok {
		return
	}
	change := wordpressSearchReplaceChange(r)
	if err := model.ValidateWordPressSearchReplace(change); err != nil {
		s.renderWordPressSearchReplace(w, r, user, site, http.StatusBadRequest, "Run a new preview before applying changes.", nil, "")
		return
	}
	if r.FormValue("confirm") != "yes" {
		s.renderWordPressSearchReplace(w, r, user, site, http.StatusBadRequest, "Confirm that you want to back up the site and apply these replacements.", nil, "")
		return
	}
	previewToken := r.FormValue("preview_token")
	if previewToken == "" {
		s.renderWordPressSearchReplace(w, r, user, site, http.StatusBadRequest, "Run a preview before applying changes.", nil, "")
		return
	}
	jobID, err := s.store.EnqueueWordPressSearchReplace(r.Context(), user, site.ID, r.FormValue("target_id"), change, previewToken)
	if err != nil {
		s.logger.Warn("queue WordPress search and replace failed", "site", site.ID)
		s.renderWordPressSearchReplace(w, r, user, site, http.StatusConflict, "The preview is expired, already used, or no longer matches these changes. Run a new preview.", nil, "")
		return
	}
	s.respondQueuedAction(w, r, user, jobID, "/sites/"+site.ID+"/wordpress/search-replace?queued=yes", "WordPress search and replace")
}

func (s *Server) wordpressSearchReplaceSite(w http.ResponseWriter, r *http.Request, user store.User) (model.Site, bool) {
	site, ok := s.authorizedSite(w, r, user, rbac.ManageWordPress)
	if !ok {
		return model.Site{}, false
	}
	if site.Kind != model.WordPress || site.Status != "active" {
		http.Error(w, "search and replace requires an active WordPress site", http.StatusBadRequest)
		return model.Site{}, false
	}
	return site, true
}

func parseWordPressSearchReplaceForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, wordpressSearchReplaceFormBytes)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid or oversized form", http.StatusBadRequest)
		return false
	}
	return true
}

func wordpressSearchReplaceChange(r *http.Request) model.WordPressSearchReplace {
	return model.WordPressSearchReplace{Search: r.FormValue("search"), Replace: r.FormValue("replace")}
}

func (s *Server) renderWordPressSearchReplace(w http.ResponseWriter, r *http.Request, user store.User, site model.Site, status int, message string, result *broker.WordPressSearchReplaceResult, previewToken string) {
	targets, err := s.store.ListBackupTargets(r.Context())
	if err != nil {
		http.Error(w, "could not load recovery storage", http.StatusInternalServerError)
		return
	}
	form := map[string]string{"search": "", "replace": "", "target_id": ""}
	if r.Method == http.MethodPost {
		form["search"], form["replace"], form["target_id"] = r.FormValue("search"), r.FormValue("replace"), r.FormValue("target_id")
	}
	data := pageData{
		Title: "Search and replace · " + site.Domain, Section: "wordpress_search_replace", User: &user,
		CSRF: s.ensureCSRF(w, r), Site: &site, CanManageWordPress: true, BackupTargets: targets,
		Form:                   form,
		WordPressSearchReplace: result, WordPressSearchReplacePreviewToken: previewToken,
	}
	if status >= 400 {
		data.Error = message
	} else if r.URL.Query().Get("queued") == "yes" {
		data.Message = "Search and replace queued. Follow its progress in Activity."
	}
	s.renderStatus(w, "wordpress_search_replace.html", status, data)
}
