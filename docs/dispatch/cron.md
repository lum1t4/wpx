# Per-site cron

Cron schedules are site-owned desired state. `CronMigration` adds
`cron_schedules` and `wordpress_cron_settings`; append it once to the migration
slice after all preceding released migrations. Each mutation stores its pending
desired row and a frozen `site.cron_apply` or `site.wordpress_cron_apply` job in
one transaction. Another cron edit for the site waits until that job finishes.
The worker calls the typed broker operation, and `FinishCronJobTx` records the
job outcome and desired-state outcome in the same transaction. A delete first
becomes `pending_delete`; WPX removes the row only after confirmed host removal.
Unknown broker outcomes requeue the same job key and payload through
`RetryCronJob`, so replay reconciles the same desired state.

The root broker receives an argument vector. Model validation accepts a command
name or absolute executable path, rejects traversal, newlines, shells and
privilege-management executables, and bounds every argument. The host generates
one `/etc/cron.d` file per enabled schedule. It puts the derived site account in
cron's user field and quotes every vector element for cron and its fixed shell;
the command never runs as root. Five-field parsing supports lists, ranges and
steps with field-specific numeric bounds. Native cron evaluates day-of-month
and day-of-week with its standard OR semantics when both are restricted.

WordPress replacement uses a separate managed cron entry containing the fixed
WP-CLI vector. Enabling installs that external trigger before inserting the
marked `DISABLE_WP_CRON` definition. If the config edit fails, WPX removes the
new trigger. Disabling removes WPX's definition before removing the external
trigger. This failure order can briefly allow both runners but does not leave
WordPress without either runner. WPX refuses an existing unmanaged
`DISABLE_WP_CRON` definition rather than taking ownership of it.

The host retains desired cron files below `<DataRoot>/cron`. Lifecycle wiring
must use these optional narrow interfaces on the provisioner:

```go
type siteCronLifecycle interface {
    SetSiteCronEnabled(context.Context, model.Site, bool) error
    RemoveSiteCron(context.Context, model.Site) error
}
```

Call `SetSiteCronEnabled(ctx, site, false)` while disabling, after traffic has
been stopped but before returning host success. Call it with `true` during
enable after the site runtime is ready. Call `RemoveSiteCron(ctx, site)` during
site deletion before deleting the Unix identity. Disabled schedules have no
retained source file and therefore are not accidentally re-enabled with the
site. A disabled site keeps sources for schedules whose desired `Enabled` value
is true, but removes every live `/etc/cron.d` entry.

Web integration requires:

```go
// in Server.Handler, after mux creation
s.registerCronRoutes(mux)

// pageData
CronSchedules          []model.CronSchedule
WordPressCronSetting   *model.WordPressCronSetting
CanManageCron          bool
```

The page is `GET /sites/{id}/cron`, uses section key `cron`, and renders
`site_cron.html`. Its mutations are registered by `registerCronRoutes`. All GET
and POST handlers use `authorizedSite(..., rbac.ManageAllSites)`, which checks
both the capability and the site's assignment. Navigation should link to
`/sites/{{.Site.ID}}/cron` only when `CanManageCron` is true.

The broker's central dispatcher calls the feature helper before its legacy
switch:

```go
if response, handled := s.dispatchCron(request); handled {
    return response
}
```

The helper recognizes `OpCronApply` and `OpWordPressCronApply`, rejects unknown
JSON fields, cross-site payloads, unsafe command vectors, wrong site kinds, and
site states other than active or disabled. `*Host` implements its optional
`cronManager` interface directly.
