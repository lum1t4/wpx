# Operating WPX

These instructions describe a normal Ubuntu 24.04 installation. Substitute your
own host, domain, and site identifiers for the examples. Keep SSH access while
changing panel access or upgrading.

## Installation and owner setup

Use a fresh Ubuntu 24.04 amd64 or arm64 server with sudo access, working DNS
resolution, and outbound HTTPS/package access. WPX manages Nginx, MariaDB, Redis,
PHP-FPM, Python, and systemd on that host. It is not an import tool for an
existing panel or a preconfigured web stack.

```sh
curl -fsSL https://github.com/lum1t4/wpx/releases/latest/download/install.sh | sudo sh
```

The installer prints a setup URL and bootstrap token after local readiness
checks succeed. Open that URL, accept the initial self-signed certificate only
for the server you are configuring, and create the owner account. Setup closes
once an owner exists. Save your password, enable TOTP under Account, and keep
the single-use recovery codes in your password manager.

The initial panel listens on TCP 9443. Website traffic uses TCP 80/443. Permit
the required ports in your VPS provider's firewall and host firewall; keep SSH
reachable. Installing the nftables package does not configure your provider's
firewall or prove that the panel is reachable from the internet.

If installation stops, retain its error output and rerun the same command.
A recognized WPX installation marker selects resume instead of upgrade. Resume
preserves existing configuration and secret material; it refuses a missing or
invalid existing encryption key rather than replacing it. Before owner setup,
use the new bootstrap token printed by the resumed attempt. Never delete
`/var/lib/wpx`, the secret key, or the installation marker to bypass an error.

## Choose panel access

Access configuration is an SSH/root operation. It is separate from a hosted
site's SSL & security page.

| Access | Command | Prerequisites |
| --- | --- | --- |
| Panel domain | `sudo wpx access --domain panel.example.com` | A/AAAA records point to this host; port 80 reachable for certificate validation, 443 for users. |
| Tailscale | `sudo wpx access --tailscale` | Tailscale already installed and authenticated, HTTPS enabled in the tailnet, appropriate ACLs. |
| SSH tunnel/local | `sudo wpx access --local` | An SSH path to the host. |
| Direct public listener | `sudo wpx access --public` | TCP 9443 reachable; uses the local self-signed certificate. |

Domain mode publishes an Nginx proxy with a Let's Encrypt certificate and moves
the panel listener to loopback. The command manages only WPX-owned proxy
configuration. Tailscale mode uses Tailscale Serve; WPX does not enroll the node
or handle your tailnet authentication credentials.

Changing away from domain mode disables the WPX Nginx proxy link while keeping
its configuration for recovery. Changing away from Tailscale removes only the
matching WPX HTTPS root route; other Serve routes and services are preserved.
Tailscale mode refuses conflicting port-443 handlers or public Funnel access.
Failed changes attempt to restore the previous listener and affected WPX routes
and report any recovery error.

For local access, run this on your workstation and open
`https://127.0.0.1:9443` while the SSH connection stays open:

```sh
ssh -N -L 9443:127.0.0.1:9443 ubuntu@server.example.com
```

After changing access, open the intended URL from the network your users will
use. A local health check cannot validate external DNS, firewall rules, or
Tailscale ACLs. If a domain certificate fails, confirm both A and AAAA records;
an obsolete AAAA record can direct validation to another host.

Panel HTTP certificate challenges use `/var/lib/wpx-acme`, a public challenge
directory separate from the private `/var/lib/wpx` state tree. An HTTP 403 during
validation can indicate a directory-traversal permission problem. Do not make
the panel's private state directory world-readable to fix certificate access.

## Daily workflow

Server navigation manages shared resources. Site navigation manages one site.

Use the domain to identify a site in the interface. New sites, staging copies,
and restored copies get an automatically generated UUID; there is no editable
site-ID field. Existing installations keep their original IDs and paths. A
domain change does not rename the site's directory, Unix account, or database.

1. In Sites, create the workload and wait for provisioning to finish. The first
   site using a PHP version may take longer because its packages are installed
   on demand. Point DNS at the host, then issue the site's certificate in
   SSL & security.
2. Configure S3-compatible storage or Google Drive in Storage. Save the restic
   repository password independently; a generated password is shown once.
3. In the site's Backups page, make and test a first snapshot, then configure
   its frequency and retention. Storage must be active before protected
   WordPress updates, restores, or staging deployment can run.
4. Use WordPress for administrator sign-in, plugin activation, updates, and
   checks. These actions run with that site's authority. A customer role can
   sign in as a WordPress administrator; assign it with that consequence in mind.
5. Use Activity to inspect failures. Retrying a failed site provisioning or
   lifecycle action preserves its site record instead of creating another site.

One-click WordPress login uses HTTPS when the site's SSL is active and HTTP
otherwise. Activate SSL before using administrator login over a public network.

Generic PHP/Python/static application deployment remains an operator task. The
file editor supports UTF-8 text up to 1 MiB, including PHP syntax checks before
save. It is not an archive uploader, package manager, or browser terminal.
Python currently starts the WSGI callable `app:application` from `public/app.py`.

## Monitor server resources

Owners and administrators can open Monitoring for server-wide CPU, memory,
swap, load averages, uptime, network rates, and filesystem capacity. The panel
samples local Linux counters every 10 seconds. Charts keep up to one hour in
memory and start again when WPX restarts. Pausing page refresh does not stop
collection, and opening extra tabs does not increase its frequency.

CPU and network rates need two valid samples. A failed read, counter reset, or
changed network interface set appears as a gap; any retained values are labeled
as the last successful reading. Network rates combine non-loopback interfaces,
so virtual interfaces can count the same traffic more than once. Filesystem
cards describe the disk containing each path, not that directory's own size.
Memory use excludes memory the kernel reports as reclaimable/available.

Use a site's Logs & usage page for its files and recent requests. Monitoring is
not per-site accounting, a quota meter, or a long-term metrics archive. Nothing
is sent to a monitoring service or stored in SQLite for these charts.

## Change a site's PHP version

An owner or administrator can change PHP under the site's Settings → PHP
version. Wait for existing site work to finish and test application compatibility
on staging first. PHP 7.1–8.5 can be selected; end-of-life versions require an
explicit acknowledgement.

WPX downloads a missing branch while the old pool serves traffic, then moves the
site's pool to the selected branch. The move can cause a brief pause for that
site. Files, databases, WordPress installation, and Nginx settings are preserved.
Other site pools keep their branch running; an unused old branch is stopped.

Activity records the result. The displayed current version changes only after
success. If the switch fails and WPX confirms the previous pool is ready, the
site returns to active on that version. If recovery cannot be confirmed, the
site shows PHP change failed; inspect Activity before retrying. A lost broker
response reuses the same job so an uncertain host change is reconciled.

The previous pool is retained as
`/etc/php/OLD_VERSION/fpm/pool.d/wpx-SITE_ID.conf.wpx-previous` outside FPM's
active configuration pattern. Keep this recovery file when investigating a
failed change. A running FPM service is not a plugin/application compatibility
test; verify the site's important pages after a successful change.

## Change a site's domain

An owner or administrator can use Settings → Website domain on an active site
with no pending operations. Point the new domain's DNS at the server first, then
enter the hostname without a scheme or path. The destination cannot already be
used or reserved by another site.

WPX briefly pauses this site's web traffic with a maintenance response while it
updates its Nginx configuration. For single-site WordPress, it keeps a local SQL
recovery copy, replaces this site's old HTTP/HTTPS URLs with serialized-data
awareness, and updates `home` and `siteurl`. Files, site identity, runtime, and
database names stay the same. Generic application configuration and external
integrations are not rewritten.

WordPress multisite, hard-coded `WP_HOME`/`WP_SITEURL` constants, or existing
WordPress URL options that do not match the site's root hostname are refused.
Persistent object caching must use WPX's managed PhpRedis integration with its
unchanged site-specific prefix; other cache backends or prefixes are refused
before rewriting. WPX invalidates this site's Redis keys, never the shared
Redis database. Unsupported configurations need a separate migration.
On sites taking orders or other live writes,
schedule the change appropriately and keep an off-server backup: the temporary
Nginx pause does not stop external cron jobs or requests already in progress.

The new domain initially serves HTTP. Existing DNS records and certificate
files are retained; neither DNS changes nor a new certificate request happen
automatically. After the domain job succeeds, use SSL & security to issue a
certificate for the new hostname. Existing staging copies retain their own
domains. Check redirects, integrations, and the application's important pages
afterwards.

Activity shows the result. Before new-domain activation begins, a failure
attempts to restore the old database and configuration. After activation begins,
retries finish the new-domain activation instead: restoring an old SQL copy
could discard writes already accepted on the new domain. An unresolved change
blocks other changes and Settings offers a retry of the same operation.
Preserve its journal and SQL recovery copy under `/var/lib/wpx/domain-changes`;
these are local recovery material, not an off-server backup, and are not
automatically pruned.

## Staging and deployment

Create staging from the production site's Staging page. Keep its generated
access password. Staging has a separate site user, database, and hostname. DNS
provider integration can create its DNS record and queue a certificate; those
are separate jobs and must succeed before trusted HTTPS works.

WPX replaces WordPress URLs with serialized-data awareness. Staging uses HTTP
basic authentication, discourages indexing, and blocks mail sent through
`wp_mail`. It does not block every payment gateway, webhook, outbound HTTP
request, or plugin-specific mail transport. Set those integrations to test mode
before using the copy.

Sync from production replaces staging content. Full deployment replaces the
production files/database; custom deployment replaces selected files and
tables. This is table replacement, not record-level conflict merging. For a
site receiving orders, signups, or comments, coordinate a write pause or avoid
replacing those tables. WPX's internal operation locks do not stop visitors or
external database writers.

Deploy creates a recovery snapshot and runs WordPress checks. Browse the live
site afterwards and exercise its critical workflow. A checksum/database check
cannot confirm that checkout or a plugin integration works.

## Disable or delete a site

Disable a site in Settings when you want to stop serving it while retaining
its files, database, and ability to re-enable it. Permanent deletion is a
separate owner-only action in Settings → Delete site.

Before deletion, make any backup you need and remove the site's staging copies.
WPX does not create a backup as part of deletion. Type the complete domain
exactly as displayed and acknowledge permanent removal. The panel rejects
deletion while another operation involving that site is pending.

| Removed after successful cleanup | Retained for separate management |
| --- | --- |
| Site files, generated Nginx/PHP/Python configuration, staging access file, dedicated Unix account, and WPX-generated WordPress database/user. | Remote backup data, stored snapshot references, remote DNS records, certificate files, logs, local recovery material outside the site tree, and separately created PHP/Python application databases. |
| Site entry, grants, schedules, and site-specific panel associations. | Job/audit history, provider credentials, and a record reserving the deleted ID against reuse. |

The site remains visible as deleting until cleanup succeeds. A failure may
leave some resources already removed: review Activity and resume the same
deletion job after fixing its cause. There is no undo, automatic reconstruction,
or deleted-site restore screen. Retained snapshots can still support an
independent recovery with their repository credentials and password.

Deleting a panel association does not remove a remote DNS record or certificate.
Review those retained resources separately; they may still be in use elsewhere.
The deletion journal under `/var/lib/wpx/deletions` is kept outside the site's
tree so retries can finish even after that tree or account has gone.
Other private recovery copies outside that tree, such as prior domain-change
SQL exports, also remain. Deletion is not an erasure of every backup or copy of
the site's data. Normal log rotation continues to apply to retained logs.

## Backups and recovery

S3-compatible targets accept a custom endpoint, region, bucket/prefix, and
bucket addressing mode. Google Drive uses rclone with credentials/token supplied
by the operator; there is no interactive Google sign-in flow in WPX. Restic
encrypts snapshots and reuses unchanged data. Repository checks are run, but a
plain `restic check` does not reread every stored data block.

| Recovery material | Includes | Does not include |
| --- | --- | --- |
| Site snapshot | Site public files, site metadata, WordPress SQL export. | Panel accounts/configuration; arbitrary PHP/Python databases; system packages. |
| WPX upgrade recovery directory | Previous panel binary, checkpointed SQLite state, WPX systemd units. | Site content/database changes or an operating-system rollback. |
| Your host backup | Whatever your backup tool explicitly captures. | Nothing is implicit; test its recovery procedure. |

Keep an independent protected backup of `/etc/wpx`, `/var/lib/wpx`, required
site data, and database exports. The panel database and `/var/lib/wpx/secret.key`
belong together for credential recovery. Do not copy a live SQLite main file
alone while ignoring its WAL; use a consistent database/host backup procedure.
Keep restic repository credentials and passwords outside the server as well.

Restore to the original site replaces its data; restore as a new site provides
a separate destination. Optional restore tests download files and import
WordPress SQL into a disposable database without switching the live site. They
do not test a browser session or promise a maximum restore time. Measure a full
restore with your actual dataset and provider before relying on a recovery-time
target.

## Updates

Read the release notes, let active operations finish, retain SSH access, and run
the installation command again. The bootstrap downloads the release binary and
verifies its checksum before invoking upgrade. An upgrade stops the panel and
broker, snapshots state, replaces the binary, and checks readiness. Hosted web
services are not intentionally stopped by the panel upgrade.

On failure, WPX attempts to restore the previous binary, SQLite, and systemd
units. If that recovery fails, the error identifies a directory under
`/var/lib/wpx/upgrades`. Preserve it and the logs. This is an attempted recovery,
not a guarantee against disk failures or interrupted host/package operations.
There is no general-purpose `wpx rollback` command.

```sh
wpx version
sudo wpx updates --status
sudo wpx updates --disable
sudo wpx updates --enable
```

Release checks contact GitHub daily when enabled. Their user agent is generic;
GitHub still receives normal connection metadata such as the server's IP.
Disabling checks does not disable functional downloads, DNS, or backups.

## When the panel is unreachable

From SSH, first separate the local application from its public proxy:

```sh
wpx version
sudo systemctl status wpx.service wpx-broker.service nginx.service --no-pager
curl --noproxy '*' -kfsS https://127.0.0.1:9443/healthz
sudo nginx -t
sudo ss -lntp
df -h / /var/lib/wpx /var/www/wpx
```

`-k` is for this local self-signed diagnostic only. If local health works but the
domain fails, inspect DNS, Nginx, certificate validity, and provider/host firewall
rules. If local health fails, inspect the panel and broker logs before changing
network configuration:

```sh
sudo journalctl --namespace=wpx -u wpx.service -u wpx-broker.service -n 100 --no-pager
sudo journalctl -u nginx.service -n 100 --no-pager
sudo systemctl status mariadb.service redis-server.service --no-pager
```

WPX units use a journal namespace. If they failed before entering it, also run
`sudo journalctl -u wpx.service -u wpx-broker.service -n 100 --no-pager`.
Site-specific Nginx logs are in `/var/log/nginx/wpx-SITE_ID-access.log` and
`/var/log/nginx/wpx-SITE_ID-error.log`; the Logs page shows a bounded recent tail.

After correcting an identified configuration/dependency issue, restart only the
affected service and repeat the local and public checks. Do not replace the
secret key, erase the state database, purge the web stack, or restore old SQLite
files into a running panel as a generic fix. An unrecoverable account/TOTP loss
currently needs deliberate operator recovery; there is no password-reset CLI.

For a bug report, include WPX version, OS/architecture, the failed operation and
Activity error, relevant redacted journal lines, and whether local health and
public access differ. Omit passwords, bootstrap tokens, recovery codes, storage
credentials, cookies, private keys, and complete configuration/database dumps.
