package web

import (
	"net/http"
	"strconv"

	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
	"github.com/lum1t4/wpx/internal/store"
)

// Provider credentials belong to server administration. DNS records belong to
// individual sites and require the corresponding site-level capability.

func (s *Server) dnsProvidersPage(w http.ResponseWriter, r *http.Request, user store.User) {
	providers, err := s.store.ListDNSProviders(r.Context())
	if err != nil {
		http.Error(w, "could not load DNS providers", http.StatusInternalServerError)
		return
	}
	s.render(w, "dns_providers.html", pageData{Title: "DNS providers", User: &user, CSRF: s.ensureCSRF(w, r), DNSProviders: providers})
}

func (s *Server) createDNSProvider(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	provider := model.DNSProvider{Name: r.FormValue("name"), Kind: model.DNSProviderKind(r.FormValue("kind")), ZoneID: r.FormValue("zone_id")}
	if provider.Kind == model.DNSCloudflare {
		provider.APIToken = r.FormValue("api_token")
	} else if provider.Kind == model.DNSRoute53 {
		provider.AccessKey, provider.SecretKey, provider.SessionToken = r.FormValue("access_key"), r.FormValue("secret_key"), r.FormValue("session_token")
	}
	if _, err := s.store.CreateDNSProvider(r.Context(), user, provider); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/dns/providers", http.StatusSeeOther)
}

func (s *Server) siteDNSPage(w http.ResponseWriter, r *http.Request, user store.User) {
	site, ok := s.authorizedSite(w, r, user, rbac.ManageDNS)
	if !ok {
		return
	}
	providers, err := s.store.ListDNSProviders(r.Context())
	if err != nil {
		http.Error(w, "could not load DNS providers", http.StatusInternalServerError)
		return
	}
	records, err := s.store.ListSiteDNSRecords(r.Context(), site.ID)
	if err != nil {
		http.Error(w, "could not load DNS records", http.StatusInternalServerError)
		return
	}
	s.render(w, "site_dns.html", pageData{Title: "DNS · " + site.Domain, User: &user, CSRF: s.ensureCSRF(w, r), Site: &site, CanManageDNS: true, DNSProviders: providers, DNSRecords: records})
}

func (s *Server) createSiteDNSRecord(w http.ResponseWriter, r *http.Request, user store.User) {
	site, ok := s.authorizedSite(w, r, user, rbac.ManageDNS)
	if !ok {
		return
	}
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	ttl, err := strconv.Atoi(r.FormValue("ttl"))
	if err != nil {
		http.Error(w, "invalid TTL", http.StatusBadRequest)
		return
	}
	record := model.DNSRecord{SiteID: site.ID, ProviderID: r.FormValue("provider_id"), Name: r.FormValue("name"), Type: r.FormValue("type"), Value: r.FormValue("value"), TTL: ttl, Proxied: r.FormValue("proxied") == "yes"}
	if _, err := s.store.CreateDNSRecord(r.Context(), user, record); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/sites/"+site.ID+"/dns", http.StatusSeeOther)
}

func (s *Server) updateSiteDNSRecord(w http.ResponseWriter, r *http.Request, user store.User) {
	site, ok := s.authorizedSite(w, r, user, rbac.ManageDNS)
	if !ok {
		return
	}
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	ttl, err := strconv.Atoi(r.FormValue("ttl"))
	if err != nil {
		http.Error(w, "invalid TTL", http.StatusBadRequest)
		return
	}
	update := model.DNSRecord{Name: r.FormValue("name"), Type: r.FormValue("type"), Value: r.FormValue("value"), TTL: ttl, Proxied: r.FormValue("proxied") == "yes"}
	if _, err := s.store.UpdateDNSRecord(r.Context(), user, site.ID, r.PathValue("record"), update); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/sites/"+site.ID+"/dns", http.StatusSeeOther)
}

func (s *Server) deleteSiteDNSRecord(w http.ResponseWriter, r *http.Request, user store.User) {
	site, ok := s.authorizedSite(w, r, user, rbac.ManageDNS)
	if !ok {
		return
	}
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	if _, err := s.store.DeleteDNSRecord(r.Context(), user, site.ID, r.PathValue("record")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/sites/"+site.ID+"/dns", http.StatusSeeOther)
}
