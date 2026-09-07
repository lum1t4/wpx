# WordPress fleet implementation

The global WordPress page is implemented by `(*web.Server).registerFleetRoutes`,
which registers `GET /wordpress` and `POST /wordpress/plugins/update`. The root
router must call `s.registerFleetRoutes(mux)` once while constructing its mux.
The shared `pageData` type needs these package-local view fields:

```go
FleetSites    []fleetSiteView
FleetOutcomes []fleetUpdateOutcome
```

`preparePageData` maps `fleet.html` to `NavSection = "wordpress"`. The global
navigation link points to `/wordpress`; the handler derives its rows from
`ListSitesForUser`, so opening the page does not expand a user's site scope.

The inventory fan-out runs at most four broker calls concurrently. Each call
has an eight-second deadline and the complete page has a twenty-second budget.
A failed or timed-out call records an error on only that site's row. Inventory
is read-only and is not persisted, so the page reports the state observed for
that request rather than implying a synchronized fleet snapshot.

The batch form submits repeated `update` values as `site-id|plugin-slug` and one
active backup target. The handler validates the grammar and rejects empty or
duplicate selections, authorizes every site, and confirms each plugin was
reported with an available update in the freshly loaded inventory before it
calls the store.
`Store.EnqueueFleetPluginUpdates` repeats actor and grant checks inside its
transaction, validates all sites and the recovery target, and checks that each
selected site had no preexisting queued or running work. Multiple plugins for
one idle site may be inserted by the same transaction; the existing single
worker processes those jobs serially.

Each accepted selection is a normal durable `wordpress.update` job with the
existing `{target_id, update}` payload. The existing worker therefore creates
the encrypted recovery snapshot, applies the plugin update, and runs WordPress
health checks. The HTTP response uses status 202 and labels every result
`Queued`; completion and failure remain visible in Activity. If validation,
authorization, or insertion fails, the enqueue transaction rolls back the
whole batch.
