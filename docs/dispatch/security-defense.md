# WordPress security defense

The site Security page keeps the existing certificate controls for every site.
WordPress sites add an opt-in defense panel for request rate limits, Fail2ban
detection, nftables bans, sensitive-path blocking, and bounded access-log
analysis. Changing protections requires the site's `site.wordpress_manage`
capability. Analysis also requires `site.logs`. The existing `site.tls`
capability remains the page-level gate.

New installations install `fail2ban` and `nftables`. Existing installations
show **Install defenses** to server administrators when the broker cannot find executable
`/usr/bin/fail2ban-client` and `/usr/sbin/nft`. That action enqueues the durable
`wordpress.security_install` job. The worker invokes a typed broker operation
whose package and service arguments are fixed in code; an HTTP request never
runs apt or systemctl directly.

Protection settings default to off. The selected sub-controls default on so an
operator can activate the full policy with one checkbox. Saving commits desired
settings with a monotonically increasing generation and queues the durable
`wordpress.security_apply` job. A second save is refused while one is queued or
running. The worker applies exactly the persisted generation; `FinishJob` marks
that same generation active or failed. Site lifecycle locking can therefore see
every pending host mutation.

Host files are:

- `/etc/nginx/conf.d/wpx-security-zones.conf`: HTTP-context maps and shared
  `limit_req_zone` definitions. Keys combine the numeric peer address and
  server name so limits stay isolated per site. Empty map keys mean unrelated
  requests do not consume rate-limit state.
- `/etc/wpx/security/sites/<site-id>.conf`: the server-context include. It is
  always present for a WordPress vhost and contains only the ownership marker
  while defense is off.
- `/etc/fail2ban/filter.d/wpx-*.conf`: fixed filters for wp-login.php,
  xmlrpc.php, `.env`/`.git`, and 404 responses.
- `/etc/fail2ban/jail.d/wpx-<site-id>.local`: per-site jails over that site's
  dedicated Nginx access log. Jails use the nftables multiport action and
  exponential `bantime.increment`, capped at one week.

Every path is absolute, clean, non-root, and derived only from a validated site
identifier. Configuration directories must be real directories. Existing files
must be regular files beginning with the security ownership marker; symlinks
and unmanaged files are refused. Applying a policy snapshots all managed files,
writes atomically, runs `nginx -t` and `fail2ban-client -t`, then reloads
Fail2ban and Nginx. A validation or reload failure restores all snapshots and
reloads the prior configuration when needed.

The security include does not set `real_ip_header`, `set_real_ip_from`, or
allow/deny policy. The site-access feature evaluates the socket peer first and
owns those directives. Root integration injects both feature includes directly
after `server_name` in generated vhosts.

Cloudflare-only origin access is incompatible with this defense mode. In that
mode the Nginx and nftables peer is a shared Cloudflare edge, so rate limiting
or banning it could disrupt unrelated visitors. Both settings paths reject the
combination until a design can authenticate a true client address for analysis
while retaining socket-peer enforcement for origin access.

Analysis opens only `/var/log/nginx/wpx-<validated-site-id>-access.log`, requires
a regular file, and reads at most 512 KiB and 2,000 lines. It accepts only
numeric IPv4/IPv6 addresses, a valid Nginx timestamp, GET/POST/HEAD request
triples, relative request targets, and HTTP status 100–599. Hostnames and
malformed lines are counted but discarded. Query strings containing a target
name do not count; classification uses the parsed path. Sources appear only
after a WordPress endpoint signal, sensitive scan, or at least four 404s. The
result is capped at 50 sources and reports low, medium, or high risk.

## Root integration contract

Append `store.SecurityMigration` to `migrations` after the preceding release's
last migration. In `broker.Server.dispatch`, call `s.dispatchSecurity(request)`
with the other feature dispatch helpers. In `web.Server.Handler`, call
`s.registerSecurityRoutes(mux)` and remove the old generic registration for
`GET /sites/{id}/security`.

Add these fields to `web.pageData`:

```go
SecuritySettings      *model.SecuritySettings
SecurityReport        *broker.SecurityReport
SecurityAvailable     bool
SecurityInstallStatus string
```

Add these fields to `provision.Host`, set the production values in
`DefaultHost`, and use temporary roots in `testHost`:

```go
SecurityNginxRoot       string // /etc/wpx/security/sites
SecurityNginxGlobal     string // /etc/nginx/conf.d/wpx-security-zones.conf
SecurityFail2banJails   string // /etc/fail2ban/jail.d
SecurityFail2banFilters string // /etc/fail2ban/filter.d
```

The shared generated-vhost hook calls
`Host.InjectSiteSecurityNginx(site, configuration)` for WordPress vhosts during
initial provisioning and certificate/domain re-rendering. `EnsureSecurityDefaults`
preserves an existing owned include and creates only missing defaults.

Add the worker switch arm:

```go
case "wordpress.security_install":
    operationErr = w.installSecurity(ctx, job)
case "wordpress.security_apply":
    operationErr = w.applySecurity(ctx, job)
```

In `Store.FinishJob`, call
`s.finishSecurityApply(ctx, tx, job, errorText, operationErr)` for
`wordpress.security_apply` before committing the general job result.

Add `"wordpress.security_install": "Install WordPress defenses"` to the job
labels. New-install package setup includes `fail2ban` and enables
`fail2ban.service`; `nftables` is already in the WPX package list.

Host-level syntax and service behavior still require a disposable Ubuntu 24.04
container or VM. Unit tests do not execute apt, Nginx, Fail2ban, nftables, or
systemctl on the development host.
