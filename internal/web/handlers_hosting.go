package web

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
	"github.com/lum1t4/wpx/internal/store"
)

func (s *Server) registerHostingRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /hosting", s.requireSession(s.requireCapability(rbac.ManageServer, s.hostingPage)))
	mux.HandleFunc("POST /hosting/mail", s.requireSession(s.requireCapability(rbac.ManageServer, s.configureMail)))
	mux.HandleFunc("GET /sites/{id}/runtime", s.requireSession(s.runtimePage))
	mux.HandleFunc("POST /sites/{id}/runtime", s.requireSession(s.configureRuntime))
	mux.HandleFunc("GET /sites/{id}/ftp", s.requireSession(s.ftpPage))
	mux.HandleFunc("POST /sites/{id}/ftp", s.requireSession(s.createFTPUser))
	mux.HandleFunc("POST /sites/{id}/ftp/{user}/delete", s.requireSession(s.deleteFTPUser))
}

func (s *Server) hostingPage(w http.ResponseWriter, r *http.Request, user store.User) {
	mail, err := s.store.MailService(r.Context())
	if err != nil {
		http.Error(w, "could not load mail service", http.StatusInternalServerError)
		return
	}
	sites, err := s.store.ListSites(r.Context())
	if err != nil {
		http.Error(w, "could not load sites", http.StatusInternalServerError)
		return
	}
	data := pageData{Title: "Hosting services", User: &user, CSRF: s.ensureCSRF(w, r), Sites: sites, MailService: &mail}
	if r.URL.Query().Get("mail") == "queued" {
		data.Message = "Postfix setup queued. Follow progress in Activity."
	}
	s.render(w, "hosting.html", data)
}
func (s *Server) configureMail(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	if _, err := s.store.ConfigureMailService(r.Context(), user, r.FormValue("hostname")); err != nil {
		s.renderHostingError(w, r, user, err.Error())
		return
	}
	http.Redirect(w, r, "/hosting?mail=queued", http.StatusSeeOther)
}
func (s *Server) renderHostingError(w http.ResponseWriter, r *http.Request, user store.User, message string) {
	mail, _ := s.store.MailService(r.Context())
	sites, _ := s.store.ListSites(r.Context())
	s.renderStatus(w, "hosting.html", http.StatusBadRequest, pageData{Title: "Hosting services", User: &user, CSRF: s.ensureCSRF(w, r), Sites: sites, MailService: &mail, Error: message, Form: map[string]string{"hostname": r.FormValue("hostname")}})
}

func (s *Server) runtimePage(w http.ResponseWriter, r *http.Request, user store.User) {
	site, ok := s.authorizedSite(w, r, user, rbac.ManageAllSites)
	if !ok {
		return
	}
	if site.Kind != model.ReverseProxy {
		http.NotFound(w, r)
		return
	}
	runtime, err := s.store.NodeRuntime(r.Context(), site.ID)
	data := pageData{Title: "Node runtime", Section: "runtime", User: &user, CSRF: s.ensureCSRF(w, r), Site: &site}
	if err == nil {
		data.NodeRuntime = &runtime
		data.Form = map[string]string{"arguments": strings.Join(runtime.Arguments, " ")}
	}
	if r.URL.Query().Get("saved") == "queued" {
		data.Message = "Node runtime setup queued. Follow progress in Activity."
	}
	s.render(w, "site_runtime.html", data)
}
func (s *Server) configureRuntime(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, ok := s.authorizedSite(w, r, user, rbac.ManageAllSites)
	if !ok {
		return
	}
	port, err := strconv.Atoi(r.FormValue("port"))
	runtime := model.NodeRuntime{SiteID: site.ID, Entrypoint: strings.TrimSpace(r.FormValue("entrypoint")), Arguments: strings.Fields(r.FormValue("arguments")), Port: port, NodeVersion: "24.20.0"}
	if err == nil {
		_, err = s.store.ConfigureNodeRuntime(r.Context(), user, site, runtime)
	}
	if err != nil {
		s.renderStatus(w, "site_runtime.html", http.StatusBadRequest, pageData{Title: "Node runtime", Section: "runtime", User: &user, CSRF: s.ensureCSRF(w, r), Site: &site, NodeRuntime: &runtime, Error: err.Error()})
		return
	}
	http.Redirect(w, r, "/sites/"+site.ID+"/runtime?saved=queued", http.StatusSeeOther)
}

func (s *Server) ftpPage(w http.ResponseWriter, r *http.Request, user store.User) {
	site, ok := s.authorizedSite(w, r, user, rbac.ManageAllSites)
	if !ok {
		return
	}
	users, err := s.store.FTPUsers(r.Context(), site.ID)
	if err != nil {
		http.Error(w, "could not load FTP users", http.StatusInternalServerError)
		return
	}
	data := pageData{Title: "FTP access", Section: "ftp", User: &user, CSRF: s.ensureCSRF(w, r), Site: &site, FTPUsers: users}
	if r.URL.Query().Get("created") == "queued" {
		data.Message = "FTP account setup queued. The password will not be shown again."
	}
	s.render(w, "site_ftp.html", data)
}
func (s *Server) createFTPUser(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, ok := s.authorizedSite(w, r, user, rbac.ManageAllSites)
	if !ok {
		return
	}
	if _, _, err := s.store.CreateFTPUser(r.Context(), user, site.ID, r.FormValue("username"), r.FormValue("password")); err != nil {
		users, _ := s.store.FTPUsers(r.Context(), site.ID)
		s.renderStatus(w, "site_ftp.html", http.StatusBadRequest, pageData{Title: "FTP access", Section: "ftp", User: &user, CSRF: s.ensureCSRF(w, r), Site: &site, FTPUsers: users, Error: err.Error(), Form: map[string]string{"username": r.FormValue("username")}})
		return
	}
	http.Redirect(w, r, "/sites/"+site.ID+"/ftp?created=queued", http.StatusSeeOther)
}
func (s *Server) deleteFTPUser(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	site, ok := s.authorizedSite(w, r, user, rbac.ManageAllSites)
	if !ok {
		return
	}
	if _, err := s.store.EnqueueFTPUserDelete(r.Context(), user, site.ID, r.PathValue("user")); err != nil {
		users, _ := s.store.FTPUsers(r.Context(), site.ID)
		s.renderStatus(w, "site_ftp.html", http.StatusBadRequest, pageData{Title: "FTP access", Section: "ftp", User: &user, CSRF: s.ensureCSRF(w, r), Site: &site, FTPUsers: users, Error: err.Error()})
		return
	}
	http.Redirect(w, r, "/sites/"+site.ID+"/ftp?delete=queued", http.StatusSeeOther)
}
