# Product and requirements coverage

This document records the agreed product scope and the current implementation.
“Implemented” means a code path exists; it does not mean the feature has passed
every live-provider, workload, and interruption scenario. Automated tests use
temporary files, SQLite, and fake command/API runners for much of the host work.
Release notes should identify any additional real-host/provider verification.

## Product boundaries

WPX is a free GPL-3.0 panel for a single server. It prioritizes efficient hosting
workflows for its operator, collaborators, and customers. Ubuntu 24.04 on amd64
and arm64 is the current platform. Other distributions remain future work; Node.js
hosting is deliberately postponed. The name remains provisional.

The panel is a static Go executable with embedded UI assets. It uses Nginx,
MariaDB, PHP-FPM, Redis, and Python on the host. An architecture-specific Linux
binary is portable only where its host dependencies and Linux interfaces are
supported. WPX itself has no APT repository or Debian package.

## User experience

Server navigation answers “what is on this server?”: Overview, Sites, Activity,
Storage, DNS providers, Users, Account. Shared credentials and user management
belong here.

Selecting a site establishes the context for Overview, WordPress, Staging,
Backups, SSL & security, Settings, Files, DNS, and Logs. Show only sections
appropriate to the site type and capabilities. Common actions stay on their own pages; advanced
configuration belongs in Settings. Success, waiting, and failure states must be
visible without reading raw command output.

The appearance is inspired by shadcn/ui: neutral surfaces, restrained borders,
clear typography, consistent controls, and visible keyboard focus. Tailwind
utilities remain directly in templates. No BEM classes, semantic styling aliases,
`@apply`, or decorative gradients.

## Coverage

| Requirement | Current implementation | Evidence and remaining limits |
| --- | --- | --- |
| One-line installation and manual upgrades | GitHub bootstrap, SHA-256 check, Ubuntu installer, resume detection, local recovery copies. | `internal/install/*_test.go`, `internal/upgrade/upgrade_test.go`; CI does not perform a full host install. |
| Static Go, amd64/arm64 | Both Linux binaries cross-built in CI and release workflow. | Runtime service configuration remains Ubuntu-specific. |
| WordPress, PHP, Python, static, proxy sites | Provisioning and enable/disable lifecycle. | `internal/provision` tests; Python is currently WSGI `app:application`. No Node.js. |
| PHP version management with low idle cost | PHP 7.1–8.5 selection at creation and in site Settings; on-demand package installation and FPM pools; unused branches stopped. | PHP model/store/worker/host tests; owner/admin switches require explicit EOL acknowledgement. Switching can briefly interrupt the changing site; application compatibility needs its own check. |
| Owner/admin/collaborator/customer | Preset capabilities and site assignments, create/edit/disable accounts. | RBAC, store, and HTTP authorization tests. No custom-role editor or ownership-transfer workflow. |
| Password and TOTP | Login, enrollment, recovery codes, own password changes, TOTP removal, session invalidation. | Store/TOTP/web tests. No forgotten-password/root recovery command. |
| Public/domain/local/Tailscale panel | Root CLI configures listener/proxy access. | `internal/panelaccess` tests. Public DNS/certificates and tailnet ACLs require host/network verification. |
| WordPress administration | Administrator magic login, core/plugin/theme inventory and updates, plugin activate/deactivate, health checks. | WordPress operation and web tests. No theme activation/removal UI; checks do not validate site-specific behavior. |
| WordPress multisite | Subdirectory/subdomain mode at creation and matching staging copies. | WordPress/model/staging tests. Subdomain routing and wildcard DNS/certificates need a real-host check. |
| WordPress cache | Redis object cache and FastCGI cache enabled by default, with per-site off switches. | Performance/provision tests. This is not a security boundary for hostile tenants. |
| Staging and cloning | Separate WordPress environment, serialized URL replacement, sync, full/custom deploy with recovery snapshot. | Staging/restore-clone tests. Table replacement is not a record-level merge. |
| Standalone search/replace | URL replacement exists inside staging/restore workflows. | A general search/replace preview/apply UI remains pending. |
| Staging protection | Password gate, indexing discouragement, `wp_mail` suppression. | Staging tests. Payments, webhooks, cron, and other outbound integrations are not universally isolated. |
| S3-compatible and Google Drive backup | Restic encryption/incremental storage, custom endpoints, rclone Drive target. | Backup/model/runner tests. Drive OAuth credentials/token are operator-supplied; no browser authorization flow. |
| Schedules, retention, restore tests | Every 6 hours/daily/weekly backup; daily/weekly/monthly retention; optional weekly/monthly restore tests. | Schedule and restore tests. No promised restore-time bound; measure a full recovery with actual data. |
| Restore to original/new/staging | Snapshot restore and WordPress database import, with operation-specific recovery. | Restore/clone tests. Generic app databases and panel state are outside a site snapshot. |
| Cloudflare and Route 53 | Provider validation, WPX-managed DNS records, DNS-01/wildcard certificate paths. | DNS/store/certificate tests. Not a full DNS-zone editor/importer. Live provider verification is separate. |
| File manager and editable PHP | Browse/read/save text; PHP syntax validation, revisions, atomic replacement and Linux path confinement. | Linux file tests. No upload/delete/rename/archive/revision-restore UI; 1 MiB UTF-8 edit limit. |
| Expert Nginx/PHP configuration | Constrained snippets with validation/reload handling. | Snippet tests. Generated host files are not the editable source of truth. |
| Local stats and logs, no telemetry | File/disk counts, bounded recent Nginx logs, Activity and local audit records. | Observability tests and log policy tests. No historical CPU/RAM/traffic charts or full audit browser. |
| GitHub release trust | Pinned Actions, cross-builds, checksums and GitHub attestations. | Workflow definitions. Repo protection/immutable-release settings must be verified separately; installer checks checksum, not attestation. |

## Roles as shipped

| Role | Scope |
| --- | --- |
| Owner | All sites and server/user administration; can grant administrator access. |
| Administrator | All sites and server administration; can manage lower-privilege users, but cannot alter the owner or peer administrators. |
| Collaborator | Assigned sites: deployment, backups, files, DNS, logs, WordPress and TLS operations. |
| Customer | Assigned sites: view, backups, logs, WordPress administrator sign-in. |

The customer preset grants real WordPress administrator access through magic
login. It should not be described as a read-only or content-editor account.
Host-root access and panel-access changes remain SSH/CLI operations.

## Recovery and operational limits

A successful queued request is not completion; Activity reports the job outcome.
The single worker requeues interrupted work on startup. Each operation owns its
retry/recovery logic, so do not promise universal exactly-once execution or
rollback after every possible interruption.

Restic snapshots are encrypted and can be used outside WPX with their repository
password and provider credentials. A site snapshot is not a full panel/server
backup. WordPress database exports are included; databases belonging to generic
PHP/Python applications need an independent procedure. File and database capture
is not atomic with external writers.

WordPress updates, restores, and deployment perform recovery/check steps, but a
green check does not validate business behavior such as checkout. The operator
must test that behavior on staging and after deployment. Details and recovery
diagnostics are in [Operating WPX](OPERATIONS.md).

## Deliberately outside the current scope

No mail hosting, browser terminal, fleet control plane, billing system,
one-click server import, or Node.js runtime. WordPress migration remains the
responsibility of an appropriate migration workflow/plugin. Do not add these
to make the panel appear more complete; improve the agreed daily workflows first.
