package web

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
	"github.com/lum1t4/wpx/internal/store"
)

const (
	fleetInventoryParallelism = 4
	fleetInventoryTimeout     = 8 * time.Second
	fleetPageTimeout          = 20 * time.Second
	fleetMaxFormBytes         = 64 << 10
)

type fleetSiteView struct {
	Site      model.Site
	Inventory broker.WordPressInventoryResult
	CanManage bool
	LoadError string
}

type fleetUpdateOutcome struct {
	SiteID string
	Domain string
	Plugin string
	Status string
}

type fleetSummaryView struct {
	Sites         int
	CoreUpdates   int
	PluginUpdates int
	ThemeUpdates  int
}

func (s *Server) registerFleetRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /wordpress", s.requireSession(s.wordpressFleetPage))
	mux.HandleFunc("POST /wordpress/plugins/update", s.requireSession(s.updateWordPressFleetPlugins))
}

func (s *Server) wordpressFleetPage(w http.ResponseWriter, r *http.Request, user store.User) {
	data, err := s.wordpressFleetData(w, r, user)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, "fleet.html", data)
}

func (s *Server) wordpressFleetData(w http.ResponseWriter, r *http.Request, user store.User) (pageData, error) {
	sites, err := s.store.ListSitesForUser(r.Context(), user)
	if err != nil {
		return pageData{}, errors.New("could not load WordPress sites")
	}
	views := make([]fleetSiteView, 0, len(sites))
	canManage := false
	for _, site := range sites {
		if site.Kind != model.WordPress {
			continue
		}
		view := fleetSiteView{
			Site:      site,
			CanManage: s.store.UserCanSite(r.Context(), user, site.ID, rbac.ManageWordPress),
		}
		canManage = canManage || view.CanManage
		views = append(views, view)
	}
	var targets []model.BackupTarget
	if canManage {
		targets, err = s.store.ListBackupTargets(r.Context())
		if err != nil {
			return pageData{}, errors.New("could not load backup targets")
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), fleetPageTimeout)
	defer cancel()
	semaphore := make(chan struct{}, fleetInventoryParallelism)
	var wait sync.WaitGroup
	for index := range views {
		if views[index].Site.Status != "active" {
			views[index].LoadError = "Inventory is available when this site is active."
			continue
		}
		if s.broker == nil {
			views[index].LoadError = "WordPress inventory is unavailable."
			continue
		}
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				views[index].LoadError = "WordPress inventory timed out."
				return
			}
			inventoryCtx, stop := context.WithTimeout(ctx, fleetInventoryTimeout)
			defer stop()
			var inventory broker.WordPressInventoryResult
			site := views[index].Site
			err := s.broker.Call(inventoryCtx, broker.OpWordPressInventory, "wordpress.fleet.inventory:"+site.ID+":"+mustRandomHex(8), broker.WordPressPluginsRequest{Site: site}, &inventory)
			if err != nil {
				s.logger.Warn("load fleet WordPress inventory", "site", site.ID, "error", err)
				if inventoryCtx.Err() != nil {
					views[index].LoadError = "WordPress inventory timed out."
				} else {
					views[index].LoadError = "WordPress inventory could not be loaded."
				}
				return
			}
			views[index].Inventory = inventory
		}(index)
	}
	wait.Wait()
	summary := fleetSummaryView{Sites: len(views)}
	for _, view := range views {
		if view.Inventory.CoreUpdateVersion != "" {
			summary.CoreUpdates++
		}
		for _, plugin := range view.Inventory.Plugins {
			if plugin.Update == "available" {
				summary.PluginUpdates++
			}
		}
		for _, theme := range view.Inventory.Themes {
			if theme.Update == "available" {
				summary.ThemeUpdates++
			}
		}
	}
	return pageData{Title: "WordPress", User: &user, CSRF: s.ensureCSRF(w, r), CanManageWordPress: canManage, BackupTargets: targets, FleetSites: views, FleetSummary: summary}, nil
}

func (s *Server) updateWordPressFleetPlugins(w http.ResponseWriter, r *http.Request, user store.User) {
	r.Body = http.MaxBytesReader(w, r.Body, fleetMaxFormBytes)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid update selection", http.StatusBadRequest)
		return
	}
	if !s.validCSRF(r) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	values := r.Form["update"]
	if len(values) == 0 {
		http.Error(w, "select at least one plugin update", http.StatusBadRequest)
		return
	}
	updates := make([]store.FleetPluginUpdate, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if strings.Count(value, "|") != 1 {
			http.Error(w, "invalid plugin update selection", http.StatusBadRequest)
			return
		}
		parts := strings.SplitN(value, "|", 2)
		update := store.FleetPluginUpdate{SiteID: parts[0], Plugin: parts[1]}
		if err := model.ValidateSiteID(update.SiteID); err != nil || model.ValidateWordPressUpdate(model.WordPressUpdate{Component: model.WordPressPlugin, Name: update.Plugin}) != nil {
			http.Error(w, "invalid plugin update selection", http.StatusBadRequest)
			return
		}
		if _, duplicate := seen[value]; duplicate {
			http.Error(w, "duplicate plugin update selection", http.StatusBadRequest)
			return
		}
		seen[value] = struct{}{}
		updates = append(updates, update)
	}

	// Authorize every submitted site before queueing any work. In particular,
	// do not let a valid first row hide a later cross-site privilege violation.
	domains := make(map[string]string, len(updates))
	for _, update := range updates {
		site, err := s.store.Site(r.Context(), update.SiteID)
		if err != nil || site.Kind != model.WordPress || !s.store.UserCanSite(r.Context(), user, site.ID, rbac.ManageWordPress) {
			http.Error(w, "permission denied", http.StatusForbidden)
			return
		}
		domains[site.ID] = site.Domain
	}

	// Prepare the response before committing jobs. Once the transaction below
	// succeeds, a failed follow-up read must not turn accepted background work
	// into an HTTP error that invites the operator to submit it again.
	data, err := s.wordpressFleetData(w, r, user)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	available := make(map[string]struct{})
	for _, site := range data.FleetSites {
		if site.LoadError != "" || !site.CanManage {
			continue
		}
		for _, plugin := range site.Inventory.Plugins {
			if plugin.Update == "available" {
				available[site.Site.ID+"\x00"+plugin.Name] = struct{}{}
			}
		}
	}
	for _, update := range updates {
		if _, ok := available[update.SiteID+"\x00"+update.Plugin]; !ok {
			http.Error(w, "selected plugin update is no longer available", http.StatusBadRequest)
			return
		}
	}
	jobs, err := s.store.EnqueueFleetPluginUpdates(r.Context(), user, r.FormValue("target_id"), updates)
	if err != nil {
		status := http.StatusBadRequest
		if err.Error() == "permission denied" {
			status = http.StatusForbidden
		}
		http.Error(w, err.Error(), status)
		return
	}
	data.Message = "Plugin updates queued. WPX will back up each site and run its update in the background."
	data.FleetOutcomes = make([]fleetUpdateOutcome, 0, len(jobs))
	for _, job := range jobs {
		data.FleetOutcomes = append(data.FleetOutcomes, fleetUpdateOutcome{SiteID: job.SiteID, Domain: domains[job.SiteID], Plugin: job.Plugin, Status: "Queued"})
	}
	s.renderStatus(w, "fleet.html", http.StatusAccepted, data)
}
