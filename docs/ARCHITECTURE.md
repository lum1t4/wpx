# Architecture

WPX manages the machine on which it runs. One executable supplies the web
service, root broker, installer, access configuration, and upgrade commands.
There is no fleet controller or background cloud service.

## Processes and authority

```text
Browser
  -> wpx serve (Unix user wpx)
       -> SQLite: identities, site records, jobs, audit events
       -> one in-process job worker
       -> /run/wpx/broker.sock
            -> wpx broker (root)
                 -> host configuration and services
                 -> site commands through runuser
```

The public process renders HTML and authorizes each request. A site permission
requires both a role capability and access to that site. Root work crosses a
versioned JSON protocol on a Unix socket. The socket is `root:wpx`, mode `0660`;
the broker checks the connecting Linux UID and validates each operation again.

The broker accepts defined operations rather than arbitrary executable names or
shell programs. Identifiers and relative file paths are constrained before
deriving host paths. External programs receive argument arrays. This reduces
the authority exposed to web handlers; it does not make a compromised panel or
root broker harmless. The web process can request the operations assigned to it
and can decrypt credentials needed for DNS and backups.

Sites have dedicated Unix users. PHP-FPM pools and Python services use those
users; Nginx receives only the access needed to serve files and reach sockets.
The file editor uses Linux `openat2` confinement, rejects symlink traversal, and
preserves site ownership. These are process/filesystem boundaries, not container
isolation between mutually hostile tenants.

## Source map

| Location | Responsibility and boundary |
| --- | --- |
| `cmd/wpx` | Command wiring, service lifetime, dependency construction. |
| `internal/web` | Sessions, CSRF, capability checks, page data, embedded templates/CSS. |
| `internal/model`, `internal/rbac` | Accepted product values and role capabilities. |
| `internal/store` | SQLite transactions, accounts, desired state, job and audit records. |
| `internal/worker` | Claim queued work, resolve persisted inputs, call host/DNS operations, record results. |
| `internal/broker` | Versioned root protocol, peer credentials, operation dispatch. |
| `internal/provision` | Nginx/PHP/Python, WordPress, files, backups, certificates, staging, local observation. |
| `internal/dns` | Cloudflare and Route 53 API requests and record ownership checks. |
| `internal/install`, `internal/platform` | Ubuntu detection, dependencies, identities, units, first-run state. |
| `internal/panelaccess`, `internal/upgrade` | Root-only reachability changes and panel binary replacement. |
| `internal/config`, `internal/updatecheck` | Process configuration and optional GitHub release discovery. |

Within `internal/web`, `server.go` constructs the server, `routes.go` registers
routes, `middleware.go` owns session/CSRF/security boundaries, `render.go` renders
templates, and `views.go` builds shared page/navigation state. The
`handlers_*.go` files group HTTP actions by user task: sites, PHP, WordPress,
staging, backups, files, DNS, accounts, users, and overview. Keep those files at the same
package boundary so the split improves reading without introducing another
framework or routing abstraction.

`cmd/wpx-preview` is a separate development executable. It uses synthetic data
and read-only request handling to display the same templates, with no privileged
broker, real worker, or provider connection. Its injected session never becomes
a production authentication mode.

`platform` currently detects and gates Ubuntu 24.04; it is not a complete
cross-distribution adapter. Package names, fixed executable paths, systemd, and
Nginx/PHP layout also appear in installation and provisioning code. Another
distribution needs explicit implementation and host tests.

## State and secrets

SQLite is authoritative for accounts, desired site settings, schedules, job
outcomes, and audit history. Generated host files are a separate layer: a
database transaction cannot roll back Nginx, MariaDB, or a remote API call.
Ownership markers protect generated configuration from silently taking over
unmanaged files. Expert changes belong in validated snippets, not generated
files that later reconciliation will replace.

Backup and DNS credentials and TOTP secrets are encrypted in SQLite with
AES-GCM using `/var/lib/wpx/secret.key`. The key is a separate, protected file on
the same server. This protects a database copy without the key, not a root
compromise or a backup containing both. Passwords and recovery codes are hashed;
session tokens are stored as hashes. Ordinary SQLite rows, job metadata, and
audit events are not an encrypted database.

Site files and MariaDB data live outside SQLite. Restic snapshots contain a
site's public tree, site metadata, and a WordPress database export when relevant.
They do not include all panel state, operating-system configuration, or arbitrary
application databases. See [backup scope](OPERATIONS.md#backups-and-recovery).

## Job lifecycle and retries

One worker runs inside `wpx serve`. It enqueues due schedules, claims one queued
job with a conditional SQL update, invokes the operation, then commits its
outcome. On process startup it returns interrupted `running` jobs to the queue.
Activities currently expose coarse waiting/running/completed/failed state, not
byte progress or reliable completion-time estimates. A long operation occupies
the worker; there is no distributed queue or worker pool.

The broker does not persist a universal result ledger for every request key.
Retry behavior belongs to each operation: backups find restic tags, several
restore operations write completion markers, provisioning converges generated
state, and file/database replacement has operation-specific recovery copies.
Do not describe this as exactly-once execution. New operations must document
what happens after the host change succeeds but before the worker saves success.
Failed jobs remain visible; supported site lifecycle retries are explicit.

## Failure boundaries

Nginx and PHP changes validate configuration before reload where implemented.
Staging, restores, and WordPress updates keep recovery data and attempt rollback
at their own boundaries. These are not a single filesystem/database transaction.
An interruption or failed recovery step can still require operator intervention.

WordPress health checks run WP-CLI installation, core checksum, and database
checks. They do not prove browser rendering, checkout, plugin compatibility, or
email delivery. A successful restore test proves files can be downloaded and,
for WordPress, SQL can be imported into a disposable database; it does not start
and browse the restored site.

Installation and upgrades check local panel/service readiness. `/healthz`
checks the web service's SQLite access. Neither that endpoint nor a systemd
active state proves public DNS, a trusted certificate, firewall reachability,
or every hosted site's behavior. Those are separate operator checks.

Upgrade recovery concerns the panel binary, SQLite, and WPX systemd units.
It is not an operating-system/package rollback or a site restore. Keep the
recovery directory if an upgrade reports a rollback error.

## Interface and network

Go templates render pages; the release embeds compiled Tailwind CSS. Node.js is
a build dependency. There is no frontend runtime framework or BEM-style CSS
component layer. Site sections share navigation, while handlers should load
only data needed for the current section.

Metrics and logs stay on the server. Functional network traffic includes
Ubuntu/PHP package sources, GitHub downloads and optional release checks,
WordPress APIs, certificate authorities, configured DNS/backup providers, and
Tailscale when enabled. These services see ordinary connection metadata. There
is no WPX analytics endpoint, and no complete outbound-traffic inventory UI.
