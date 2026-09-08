# Dedicated navigation

The shared shell renders one navigation context at a time. Pages with a `Site`
show the site domain, a link back to `/sites`, authorized site tools, and the
account menu in the shared header. They do not show server overview, activity,
monitoring, or global settings. Pages without a `Site` retain the server
navigation and global settings.

Site tools use stable `Section` values so direct pages and validation responses
mark the same link current. The coordinated additions are `access`, `cron`,
`runtime`, and `ftp`, at `/sites/{id}/access`, `/sites/{id}/cron`,
`/sites/{id}/runtime`, and `/sites/{id}/ftp`. Access and FTP use the existing
`CanManageSites` display flag. Runtime uses the same flag and appears only for
reverse-proxy sites. Cron uses `CanManageCron`, populated from
`rbac.ManageAllSites` by `preparePageData`.

The server sidebar adds WordPress fleet management at `/wordpress`, hosting at
`/hosting`, and alerts at `/alerts`. Their `NavSection` values are `wordpress`,
`hosting`, and `alerts`. Hosting and alerts use the existing
`CanManageServer` flag; the fleet page is visible to every signed-in user and
scopes its contents to that user's assigned sites and capabilities.

The base shell loads `/assets/theme.js` before the stylesheet so the saved
appearance applies before paint, then loads `/assets/actions.js` with `defer`
for progressively enhanced quick actions. Server-side form behavior remains
authoritative. The account settings page contains language and appearance
preferences. The document language and text direction follow `Locale`.

Account settings are available from the icon-only header menu on server and
site pages. The sidebars contain no account or language controls.

`navigation_test.go` checks the rendered desktop sidebar in both contexts. The
site assertion rejects every global server destination, while the server
assertion requires global destinations and rejects site-specific tools.
