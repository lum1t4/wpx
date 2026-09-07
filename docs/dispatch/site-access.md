# Per-site access controls

WPX can require HTTP Basic authentication and can restrict an individual
origin to Cloudflare proxy connections. Both settings are desired state in
SQLite and are applied by the durable `site.access_apply` job.

## Secret and authorization boundary

Only owners and administrators can reach `GET|POST /sites/{id}/access`. The
route requires `rbac.ManageAllSites`, then loads the site through
`authorizedSite` with the same capability. This matches the server-management
and site-authorization boundary used for other privileged site settings.

Passwords are accepted only on the POST request, checked at 12–72 bytes, and
immediately converted to bcrypt cost 12. SQLite, jobs, audit details, broker
payloads, and Nginx configuration never receive plaintext. The generated
`0640 root:www-data` access password file contains only the username and bcrypt hash;
the panel never renders either the submitted password or its hash. Usernames
are limited to 1–64 ASCII letters, digits, dots, dashes, and underscores, so a
username cannot add another htpasswd record or an Nginx directive.

The SQLite transaction records the desired flags, username/hash, a pending
status, the job, and a non-secret audit event. Host failure marks the access
row failed but does not revert desired settings. Saving again retries the
current intent. This is deliberate: SQLite cannot be rolled back together with
Nginx, and silently showing old settings after a failed apply would misstate
what the operator requested.

Only one access change may be pending for a site. Both queued and running work
keep the row pending, and the desired-state UPSERT changes a row only when its
status is no longer pending. This transactionally serializes rapid saves: an
older completion cannot mark newer settings active because newer settings
cannot be accepted until that completion is recorded.

## Cloudflare ranges and direct-origin checks

Ranges come from Cloudflare's unauthenticated Version 4 endpoint,
`https://api.cloudflare.com/client/v4/ips`, documented at
`https://developers.cloudflare.com/api/resources/ips/methods/list/`. The
endpoint URL, HTTP client, and cache TTL are configurable on `provision.Host`.
Responses must be successful, bounded, canonical CIDRs and contain at least one
IPv4 and one IPv6 range. A validated cache is used for 24 hours by default. A
failed refresh uses the last validated cache; if no cache exists it uses the
official range set bundled on 2026-09-08. Empty or malformed responses can
never create an empty allowlist or allow all traffic.

Nginx `allow` rules operate on the connecting socket peer. WPX does not enable
`real_ip_header`, and the site access and security includes must remain before
any future real-IP rewriting. This ensures a forged `CF-Connecting-IP` or
`X-Forwarded-For` header cannot make a direct connection look like a Cloudflare
peer. If real-IP restoration is added later, the Cloudflare decision must first
be preserved from `$realip_remote_addr` and covered by a host test.

Cloudflare-only access and WPX's WordPress security defense cannot be enabled
together in this release. Defense rate limits and Fail2ban consume the socket
peer address, which is a shared Cloudflare edge for proxied traffic; enabling
both could rate-limit unrelated visitors or ban an edge. Each store mutation
checks the other setting so the restriction holds in both transition orders.

The access include is `/etc/wpx/access/sites/<siteID>.conf`; its bcrypt file is
beside it. Existing HTTP and TLS vhosts receive the include on their first
access apply. New and reconciled vhosts receive it through the host decoration
hook. Every changed managed file is snapshotted in memory. WPX writes the
candidate, runs `nginx -t`, and reloads. Validation failure restores all files.
Reload failure restores them, validates the restored graph, and reloads again;
an unsuccessful recovery is reported explicitly.

HTTP-01 paths are intentionally exempt from both Basic authentication and the
Cloudflare allowlist with `auth_basic off; allow all`. This preserves emergency
HTTP certificate issuance and renewal. Enabling Cloudflare-only mode does not
therefore make the entire origin private; `/.well-known/acme-challenge/` remains
public and serves only files from the managed challenge directory. DNS-01 is
still preferred for Cloudflare-only sites.

## Shared wiring

The central broker dispatch calls the optional extension:

```go
if response, handled := s.dispatchSiteAccess(request); handled {
    return response
}
```

The worker switch calls:

```go
case "site.access_apply":
    operationErr = w.applySiteAccess(ctx, job)
```

`Store.FinishJob` updates only apply metadata and retains desired fields:

```go
if job.Kind == "site.access_apply" {
    accessStatus, lastError := "active", ""
    if operationErr != nil {
        accessStatus, lastError = "failed", operationErr.Error()
    }
    _, err := tx.ExecContext(ctx,
        `UPDATE site_access_settings SET status=?,last_error=?,updated_at=? WHERE site_id=?`,
        accessStatus, lastError, now, job.TargetID)
    if err != nil { return err }
}
```

Shared construction and rendering add:

- `SiteAccessMigration` to the migration list.
- `SiteAccess *model.SiteAccessSettings` to `web.pageData`.
- `s.RegisterSiteAccessRoutes(mux)` in `Server.Handler`.
- `AccessRoot`, `CloudflareRangesURL`, `CloudflareHTTPClient`, and
  `CloudflareRangesTTL` to `provision.Host`, with production defaults
  `/etc/wpx/access/sites`, the official endpoint, nil, and 24 hours.
- `EnsureAccessDefaults` and `InjectSiteAccessNginx` to the single site-vhost
  decoration path used by provisioning, enable, domain, TLS, and expert config.
  The panel and phpMyAdmin vhosts must not use this decoration.

The site navigation uses section `access`, path `/sites/{id}/access`, existing
flag `.CanManageSites`, and template `site_access.html`.
