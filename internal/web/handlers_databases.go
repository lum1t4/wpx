package web

import (
	"errors"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
	"github.com/lum1t4/wpx/internal/store"
)

func (s *Server) databasesPage(w http.ResponseWriter, r *http.Request, user store.User) {
	s.renderDatabases(w, r, user, http.StatusOK, "", nil)
}

func (s *Server) createDatabase(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	if _, _, err := s.store.CreateDatabase(r.Context(), user, r.FormValue("site_id"), r.FormValue("label")); err != nil {
		s.renderDatabases(w, r, user, http.StatusBadRequest, err.Error(), nil)
		return
	}
	http.Redirect(w, r, "/databases?created=queued", http.StatusSeeOther)
}

func (s *Server) installDatabaseAdmin(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	if _, err := s.store.EnqueueDatabaseAdminInstall(r.Context(), user); err != nil {
		s.renderDatabases(w, r, user, http.StatusBadRequest, err.Error(), nil)
		return
	}
	http.Redirect(w, r, "/databases?phpmyadmin=queued", http.StatusSeeOther)
}

func (s *Server) revealDatabase(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	database, err := s.store.Database(r.Context(), r.PathValue("id"))
	if err != nil || database.Status != "active" {
		s.renderDatabases(w, r, user, http.StatusBadRequest, "database credentials are unavailable until creation succeeds", nil)
		return
	}
	if err := s.store.RecordDatabaseAccess(r.Context(), user, "database.credentials_viewed", "database", database.ID); err != nil {
		http.Error(w, "could not record credential access", http.StatusInternalServerError)
		return
	}
	s.renderDatabases(w, r, user, http.StatusOK, "", &database)
}

func (s *Server) deleteDatabase(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	if _, err := s.store.EnqueueDatabaseDelete(r.Context(), user, r.PathValue("id"), r.FormValue("confirmation")); err != nil {
		s.renderDatabases(w, r, user, http.StatusBadRequest, err.Error(), nil)
		return
	}
	http.Redirect(w, r, "/databases?delete=queued", http.StatusSeeOther)
}

func (s *Server) openManagedDatabase(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	database, err := s.store.Database(r.Context(), r.PathValue("id"))
	if err != nil || database.Status != "active" {
		s.renderDatabases(w, r, user, http.StatusBadRequest, "database is not active", nil)
		return
	}
	location, failureStatus, err := s.prepareDatabaseOpen(r, user, broker.DatabaseOpenRequest{Database: &database})
	if err != nil {
		s.renderDatabases(w, r, user, failureStatus, err.Error(), nil)
		return
	}
	http.Redirect(w, r, location, http.StatusSeeOther)
}

func (s *Server) openPrimaryDatabase(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, ok := s.authorizedDatabaseSite(w, r, user)
	if !ok {
		return
	}
	if site.Kind != model.WordPress || site.Status != "active" {
		s.renderSiteDatabases(w, r, user, site, http.StatusBadRequest, "WordPress database is unavailable", nil)
		return
	}
	location, failureStatus, err := s.prepareDatabaseOpen(r, user, broker.DatabaseOpenRequest{Site: &site})
	if err != nil {
		s.renderSiteDatabases(w, r, user, site, failureStatus, err.Error(), nil)
		return
	}
	http.Redirect(w, r, location, http.StatusSeeOther)
}

func (s *Server) prepareDatabaseOpen(r *http.Request, user store.User, request broker.DatabaseOpenRequest) (string, int, error) {
	status, err := s.store.DatabaseAdminStatus(r.Context())
	if err != nil {
		return "", http.StatusInternalServerError, errors.New("could not load phpMyAdmin status")
	}
	if status != "active" {
		return "", http.StatusBadRequest, errors.New("install phpMyAdmin before opening a database")
	}
	var result broker.DatabaseOpenResult
	if err := s.broker.Call(r.Context(), broker.OpDatabaseOpen, "database.open:"+mustRandomHex(16), request, &result); err != nil || result.Token == "" {
		s.logger.Warn("prepare phpMyAdmin sign-in", "error", err)
		return "", http.StatusBadGateway, errors.New("phpMyAdmin sign-in could not be prepared")
	}
	targetType, targetID := "", ""
	if request.Site != nil {
		targetType, targetID = "site", request.Site.ID
	} else {
		targetType, targetID = "database", request.Database.ID
	}
	if err := s.store.RecordDatabaseAccess(r.Context(), user, "database.phpmyadmin_opened", targetType, targetID); err != nil {
		return "", http.StatusInternalServerError, errors.New("could not record database access")
	}
	return "/phpmyadmin/wpx-signon.php?token=" + url.QueryEscape(result.Token), 0, nil
}

func (s *Server) createSiteDatabase(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, ok := s.authorizedDatabaseSite(w, r, user)
	if !ok {
		return
	}
	if !allowSiteMutation(w, r, site) {
		return
	}
	if _, _, err := s.store.CreateDatabase(r.Context(), user, site.ID, r.FormValue("label")); err != nil {
		s.renderSiteDatabases(w, r, user, site, http.StatusBadRequest, err.Error(), nil)
		return
	}
	http.Redirect(w, r, "/sites/"+url.PathEscape(site.ID)+"/databases?database=queued", http.StatusSeeOther)
}

func (s *Server) revealSiteDatabase(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, database, ok := s.authorizedSiteDatabase(w, r, user)
	if !ok {
		return
	}
	if database.Status != "active" {
		s.renderSiteDatabases(w, r, user, site, http.StatusBadRequest, "database credentials are unavailable until creation succeeds", nil)
		return
	}
	if err := s.store.RecordDatabaseAccess(r.Context(), user, "database.credentials_viewed", "database", database.ID); err != nil {
		http.Error(w, "could not record credential access", http.StatusInternalServerError)
		return
	}
	s.renderSiteDatabases(w, r, user, site, http.StatusOK, "", &database)
}

func (s *Server) openSiteDatabase(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, database, ok := s.authorizedSiteDatabase(w, r, user)
	if !ok {
		return
	}
	if database.Status != "active" {
		s.renderSiteDatabases(w, r, user, site, http.StatusBadRequest, "database is not active", nil)
		return
	}
	location, failureStatus, err := s.prepareDatabaseOpen(r, user, broker.DatabaseOpenRequest{Database: &database})
	if err != nil {
		s.renderSiteDatabases(w, r, user, site, failureStatus, err.Error(), nil)
		return
	}
	http.Redirect(w, r, location, http.StatusSeeOther)
}

func (s *Server) deleteSiteDatabase(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, database, ok := s.authorizedSiteDatabase(w, r, user)
	if !ok {
		return
	}
	if !allowSiteMutation(w, r, site) {
		return
	}
	if _, err := s.store.EnqueueDatabaseDelete(r.Context(), user, database.ID, r.FormValue("confirmation")); err != nil {
		s.renderSiteDatabases(w, r, user, site, http.StatusBadRequest, err.Error(), nil)
		return
	}
	http.Redirect(w, r, "/sites/"+url.PathEscape(site.ID)+"/databases?database-delete=queued", http.StatusSeeOther)
}

func (s *Server) authorizedDatabaseSite(w http.ResponseWriter, r *http.Request, user store.User) (model.Site, bool) {
	site, err := s.store.Site(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return model.Site{}, false
	}
	if !s.store.UserCanSite(r.Context(), user, site.ID, rbac.ManageDatabases) {
		http.Error(w, "permission denied", http.StatusForbidden)
		return model.Site{}, false
	}
	return site, true
}

func (s *Server) authorizedSiteDatabase(w http.ResponseWriter, r *http.Request, user store.User) (model.Site, model.Database, bool) {
	site, ok := s.authorizedDatabaseSite(w, r, user)
	if !ok {
		return model.Site{}, model.Database{}, false
	}
	database, err := s.store.Database(r.Context(), r.PathValue("database"))
	if err != nil || database.SiteID != site.ID {
		http.NotFound(w, r)
		return model.Site{}, model.Database{}, false
	}
	return site, database, true
}

func (s *Server) renderSiteDatabases(w http.ResponseWriter, r *http.Request, user store.User, site model.Site, status int, message string, selected *model.Database) {
	databases, databaseErr := s.store.ListDatabasesForSite(r.Context(), site.ID)
	adminStatus, adminErr := s.store.DatabaseAdminStatus(r.Context())
	if databaseErr != nil || adminErr != nil {
		http.Error(w, "could not load site databases", http.StatusInternalServerError)
		return
	}
	if selected != nil && selected.SiteID != site.ID {
		selected = nil
	}
	data := pageData{Title: site.Domain, Section: "databases", User: &user, CSRF: s.ensureCSRF(w, r), Site: &site, CanManageDatabases: true, Databases: databases, DatabaseAdminStatus: adminStatus, SelectedDatabase: selected}
	if message != "" {
		data.Error = message
	}
	s.renderStatus(w, "site.html", status, data)
}

func (s *Server) proxyDatabaseAdmin(w http.ResponseWriter, r *http.Request, _ store.User) {
	status, err := s.store.DatabaseAdminStatus(r.Context())
	if err != nil || status != "active" {
		http.Error(w, "phpMyAdmin is not installed", http.StatusServiceUnavailable)
		return
	}
	upstream, _ := url.Parse("http://127.0.0.1:9081")
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	director := proxy.Director
	externalHost := r.Host
	proxy.Director = func(request *http.Request) {
		director(request)
		request.URL.Path = strings.TrimPrefix(request.URL.Path, "/phpmyadmin")
		if request.URL.Path == "" {
			request.URL.Path = "/"
		}
		request.URL.RawPath = ""
		request.Host = upstream.Host
		request.Header.Del("X-Forwarded-For")
		request.Header.Del("X-Forwarded-Host")
		request.Header.Del("X-Forwarded-Proto")
		request.Header.Set("X-Forwarded-Host", externalHost)
		request.Header.Set("X-Forwarded-Proto", "https")
		request.Header.Set("Cookie", databaseAdminCookies(request.Header.Get("Cookie")))
	}
	proxy.ModifyResponse = scopeDatabaseAdminCookies
	proxy.ErrorHandler = func(response http.ResponseWriter, _ *http.Request, err error) {
		s.logger.Warn("phpMyAdmin proxy", "error", err)
		http.Error(response, "phpMyAdmin is temporarily unavailable", http.StatusBadGateway)
	}
	proxy.ServeHTTP(w, r)
}

// scopeDatabaseAdminCookies is the HTTPS boundary for the loopback-only
// phpMyAdmin upstream. PHP sees a plain HTTP FastCGI hop and can consequently
// omit Secure even while choosing a __Secure- cookie name from the forwarded
// public scheme. Browsers must reject that invalid cookie, which gives each
// request a fresh phpMyAdmin session and makes its CSRF token appear stale.
//
// Keep every upstream cookie inside the mounted application, add the attributes
// required by the public HTTPS origin, and never let the upstream replace a WPX
// authentication cookie on their shared origin.
func scopeDatabaseAdminCookies(response *http.Response) error {
	// Older installed sign-on scripts may not include a language parameter.
	// Rewriting at the proxy boundary makes upgrades clear stale language
	// preferences without requiring phpMyAdmin to be reinstalled.
	if response.Request != nil && response.Request.URL != nil && response.Request.URL.Path == "/wpx-signon.php" && response.StatusCode >= 300 && response.StatusCode < 400 {
		response.Header.Set("Location", "/phpmyadmin/index.php?lang=en")
	}
	cookies := response.Cookies()
	if len(cookies) == 0 {
		return nil
	}
	response.Header.Del("Set-Cookie")
	for _, cookie := range cookies {
		if cookie.Name == "wpx_session" || cookie.Name == "wpx_csrf" {
			continue
		}
		cookie.Domain = ""
		cookie.Path = "/phpmyadmin/"
		cookie.Secure = true
		cookie.SameSite = http.SameSiteStrictMode
		response.Header.Add("Set-Cookie", cookie.String())
	}
	return nil
}

func databaseAdminCookies(header string) string {
	var allowed []string
	for _, cookie := range strings.Split(header, ";") {
		cookie = strings.TrimSpace(cookie)
		name, _, found := strings.Cut(cookie, "=")
		if !found || name == "wpx_session" || name == "wpx_csrf" {
			continue
		}
		allowed = append(allowed, cookie)
	}
	return strings.Join(allowed, "; ")
}

func (s *Server) renderDatabases(w http.ResponseWriter, r *http.Request, user store.User, status int, message string, selected *model.Database) {
	databases, databaseErr := s.store.ListDatabases(r.Context())
	sites, siteErr := s.store.ListSitesForUser(r.Context(), user)
	adminStatus, adminErr := s.store.DatabaseAdminStatus(r.Context())
	if databaseErr != nil || siteErr != nil || adminErr != nil {
		http.Error(w, "could not load databases", http.StatusInternalServerError)
		return
	}
	data := pageData{Title: "Databases", User: &user, CSRF: s.ensureCSRF(w, r), Databases: databases, Sites: sites, DatabaseAdminStatus: adminStatus, SelectedDatabase: selected}
	if message != "" {
		data.Error = message
	}
	if r.URL.Query().Get("created") == "queued" {
		data.Message = "Database creation is queued. Activity will show when it is ready."
	} else if r.URL.Query().Get("delete") == "queued" {
		data.Message = "Database deletion is queued."
	} else if r.URL.Query().Get("phpmyadmin") == "queued" {
		data.Message = "phpMyAdmin installation is queued. It runs with on-demand PHP workers and will appear here when ready."
	}
	s.renderStatus(w, "databases.html", status, data)
}
