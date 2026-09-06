# Verification record

This record separates automated coverage, development previews, and real host
observations. Results apply to the candidate and environment described here;
they are not a claim that every supported workflow has been exercised live.

## 2026-09-07: databases and phpMyAdmin

Environment: existing Ubuntu 24.04 amd64 VPS. The candidate was installed with
WPX's recoverable upgrade path. A disposable additional database and SQL user
were created through authenticated panel requests and removed after the test.

| Check | Observation |
| --- | --- |
| Optional installation | The durable job downloaded the pinned official phpMyAdmin 5.2.3 archive, validated PHP-FPM/Nginx, and completed. |
| Listener boundary | The phpMyAdmin Nginx server listened only on `127.0.0.1:9081`; a request to the host address on that port was refused. The public panel path redirected an unauthenticated request to WPX login. |
| SQL scope | The generated user created/read a table in its own database. A cross-database `CREATE DATABASE` was denied. |
| Additional database sign-on | The panel's one-click handoff reached authenticated phpMyAdmin and showed only the generated database. The one-use token file was consumed. |
| WordPress sign-on | The same flow opened the existing WordPress database without exposing the additional database. |
| Restart | A fresh sign-on succeeded after restarting WPX, broker, Nginx, and PHP 8.4 FPM. |
| Durable deletion | Exact-confirm deletion removed the panel record, MariaDB schema, and SQL user; the job completed successfully. |
| Existing workloads | WPX health remained ready and the existing website continued returning HTTP 200. |

The first sign-on exposed a real isolation error: phpMyAdmin's dedicated user
could not traverse WPX's private `/var/lib/wpx` parent to reach its session
directory. The corrected candidate uses the separate protected
`/var/lib/wpx-phpmyadmin` state root; the repeated sign-on and restart checks
above then passed. Test session/cookie files, the disposable database/user, and
the abandoned pre-fix state directory were removed. phpMyAdmin itself remains
installed because it is the requested feature.

The browser Google Drive flow passed unit/integration tests with a fake token
endpoint, including PKCE, encrypted pending credentials, exact callback, token
validation, target creation, and state replay rejection. A live Google consent
and Drive repository initialization was not performed because no operator OAuth
client was supplied.

## 2026-09-06: panel domain recovery

Environment: Ubuntu 24.04, Linux amd64, an existing WPX installation on a VPS.
The domain and host address are omitted because they are not needed to reproduce
the failure. Checks below completed at approximately 19:18 UTC.

| Check | Observation |
| --- | --- |
| Original certificate failure | Let's Encrypt HTTP validation received 403. Nginx could not traverse the `0750` private data directory containing the former challenge root. |
| Candidate access repair | A helper using the updated panel-access implementation successfully issued a Let's Encrypt certificate. |
| Public panel request | Trusted HTTPS request to the panel login returned 200. |
| Public health request | HTTPS `/healthz` returned `{"ok":true}`. |
| Listener scope | The panel's port 9443 listener was loopback-only; Nginx listened on 443. |
| State/challenge permissions | Private `/var/lib/wpx` remained `0750`, owned by `wpx:wpx`. Separate `/var/lib/wpx-acme` was `0755`, root-owned. |
| Services | WPX, broker, and Nginx were active. |
| Certificate | Newly issued certificate expires 2026-12-05. Renewal was not exercised by waiting for an actual renewal. |

This verifies the permission diagnosis and public domain-access repair on that
host. At the time of these checks, the installed panel UI was still
`v0.1.0-alpha.3`; the full redesigned candidate had not yet been deployed there.
It does not establish that the new UI, PHP switching, backups, or every other
hosting workflow passed a live test.

## 2026-09-06: fresh installation and resume

A disposable Ubuntu 24.04 ARM64 systemd container exercised a fresh installation
of the candidate. Installation, the five core services, local HTTPS health,
owner setup, and the authenticated dashboard passed.

Resuming a recognized interrupted installation preserved the encryption key,
TLS certificate/key, configuration, existing owner/session, and loopback access.
Only the bootstrap token hash rotated. Missing configuration or encryption key
with an existing SQLite state correctly aborted instead of replacing original
state. The disposable container and its test data were removed afterwards.

This exercises real installation and systemd behavior on ARM64. It does not
establish every VPS firewall/network configuration or all package architectures.

## 2026-09-06: PHP runtime switching

Environment: disposable Docker host, Ubuntu 24.04.4 LTS, aarch64. A direct
`provision.DefaultHost` harness exercised real Nginx and PHP-FPM; this was not
the full browser → job worker → broker path.

| Check | Observation |
| --- | --- |
| First-use installation and switch | PHP 8.4.25 → 8.5.10 succeeded; the dynamic HTTP fixture returned the new version. |
| Shared branch | The neighboring site's PHP 8.4 pool continued serving and kept its service active. |
| Interrupted-result replay | Repeating the same previous-version operation succeeded. |
| Last pool moved | PHP 8.4 became inactive and disabled; PHP 8.5 remained active and enabled. |
| Injected Nginx validation failure | An attempted 8.5 → 8.4 change restored the 8.5 service/socket and reported confirmed recovery; both sites returned HTTP 200. |
| Neighbor continuity | 484 sampled requests across installation/switch, replay, last-pool move, and rollback had zero errors. |
| Content/config preservation | Nginx configuration and application index files retained their SHA-256 hashes. |
| Final configuration | `nginx -t` and `php-fpm8.5 -t` passed. |
| Permissions | Root `0711`, site directory `0750`, index `0640`, socket `0660` owned by site user with `www-data` group, recovery sidecar `0600`. |

The first real test exposed an invalid `php8.5-opcache` package dependency.
PHP 8.5's built-in OPcache does not need that separate package; the corrected
candidate was rebuilt and the checks above passed. The disposable container was
removed after testing.

These observations do not prove zero interruption for the changing site,
WordPress/plugin compatibility, all other PHP branches, arbitrary daemon-kill
timing, or a runtime change on the user's existing VPS.

## 2026-09-06: integrated candidate checks

The final combined command completed successfully using Docker Go 1.26 on
Linux ARM64:

```sh
go test -race ./...
go vet ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o dist/wpx-linux-amd64 ./cmd/wpx
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o dist/wpx-linux-arm64 ./cmd/wpx
```

Tailwind CSS was rebuilt and its generated diff checked. Shell syntax and the
direct-utility styling checks passed; no BEM, `@apply`, or decorative gradients
remain in the checked template/style sources. Tests cover navigation scope,
form-state retention, PHP authorization/queue/recovery behavior, and the
separate preview's read-only boundary, alongside the existing suite.

The native macOS ARM64 preview built successfully. Sixteen main preview GET
routes returned 200. Firefox desktop views of Overview, Staging, and WordPress
were visually reviewed; staging and navigation were also checked at 412 × 915.
These use sample data and do not establish production form submission behavior.

An existing WordPress site on the VPS returned HTTP 200 through local Nginx with
its Host header. Its public DNS was missing, so this was not a public-browser
WordPress verification.

## 2026-09-06: release and VPS upgrade

Release [v0.1.0-alpha.4](https://github.com/lum1t4/wpx/releases/tag/v0.1.0-alpha.4)
was published from source commit
`ec1d3e06808e63c8adfde38def08a53379777c9b`.
GitHub [Verify](https://github.com/lum1t4/wpx/actions/runs/34055073457) passed in
3 minutes 45 seconds and
[Release](https://github.com/lum1t4/wpx/actions/runs/34055350680) passed in
3 minutes 55 seconds. The release contains static Linux amd64/arm64 binaries,
checksums, bootstrap, Go module inventory, and GitHub attestations.

At approximately 19:40 UTC, the existing Ubuntu 24.04 amd64 VPS was upgraded
using the exact installation command in README:

```sh
curl -fsSL https://github.com/lum1t4/wpx/releases/latest/download/install.sh | sudo sh
```

| Check | Observation |
| --- | --- |
| Installed version | `wpx version` reported `v0.1.0-alpha.4` and source commit `ec1d3e06808e63c8adfde38def08a53379777c9b`. |
| Recovery snapshot | Upgrade created `/var/lib/wpx/upgrades/20260906T194034.307770545Z`. |
| Configuration and secrets | Configuration, encryption-key, and panel TLS file hashes were unchanged from before the upgrade. |
| Services | WPX, broker, Nginx, MariaDB, and Redis were active. |
| Access | Panel port 9443 remained loopback-only behind Nginx on 443; trusted public HTTPS health passed. |
| Existing WordPress workload | Local HTTP through Nginx returned 200. |
| Existing owner session | The browser session survived the upgrade. |
| Live UI | Sites and Logs & usage displayed correctly in the authenticated browser. |

This confirms deployment of the redesigned release on the VPS and continuity
of the checks listed above. Authenticated page review was extended after the
alpha.5 update below. The disposable-host PHP tests and sample-data visual review
remain separate evidence. See [requirements coverage](PRODUCT.md) for feature limits.

## 2026-09-06: inventory correction and alpha.5 deployment

Live testing after alpha.4 found a WordPress inventory parsing failure: WP-CLI
returned boolean `false` in a Redis drop-in's `update` field, where WPX expected
a string. The whole inventory response was rejected even though WordPress
worked. The correction handles that response shape and includes a regression
test; drop-ins do not display ordinary plugin activation toggles.

Release [v0.1.0-alpha.5](https://github.com/lum1t4/wpx/releases/tag/v0.1.0-alpha.5)
was published from source commit
`bff494ac38f70a05eb20a758195050ecddff2f79`.
GitHub [Verify](https://github.com/lum1t4/wpx/actions/runs/34055873224) passed in
3 minutes 48 seconds.
[Release](https://github.com/lum1t4/wpx/actions/runs/34055874198) passed in
3 minutes 51 seconds, including race tests, vet, CSS checks, both static Linux
builds, attestations, and bootstrap publication.

At approximately 19:49 UTC, the README latest-release installation command
successfully upgraded the VPS again.

| Check | Observation |
| --- | --- |
| Installed version | `wpx version` confirmed `v0.1.0-alpha.5`. |
| Recovery snapshot | Upgrade created `/var/lib/wpx/upgrades/20260906T194947.458197478Z`. |
| Configuration and secrets | Configuration, encryption-key, and panel TLS hashes again matched their pre-upgrade values. |
| Services and access | All five core services were active; trusted public panel health passed. |
| Existing WordPress workload | Local HTTP through Nginx still returned 200. |
| Live WordPress inventory | Authenticated Firefox displayed core version 7.1, four plugin entries including Redis and its drop-in, and three themes. The drop-in had no activation toggle. |
| Other authenticated pages | Overview, Files directory listing, PHP Settings, the empty Staging form, and Logs were reviewed. |
| Cleanup | The temporary repair helper and preview state were removed after verification. |

The live page checks were read-only. Form submissions, plugin changes, staging
creation/deployment, file saves, and PHP switching were not exercised on the
existing VPS. The WordPress hostname still had no public DNS answer, so public
browser access to that site remains unverified. These limits do not change the
separate disposable-host and automated results above.

## 2026-09-06: pre-release site lifecycle and monitoring checks

These checks concern the candidate adding automatic site UUIDs, domain changes,
permanent deletion, and local monitoring. At this stage, it had not yet been
released or deployed to the production VPS; no production mutation was performed.
Final release and deployment results follow this section.

Integrated `go test -race ./...`, `go vet ./...`, and static Linux amd64/arm64
builds passed before the final site-scoped Redis invalidation correction.
Focused domain race tests and vet passed after that correction. CI verification
of the exact final source was still pending; the earlier full-suite result is not
a claim that it covered that last change. The final provision race suite also
passed in 7.422 seconds; the final web race rerun was still pending at this entry.

Desktop and 390 × 844 visual checks covered Monitoring, new-site creation, and
Settings. New-site forms had no editable site-ID input. These visual checks do
not establish successful form submissions.

A disposable Ubuntu 24.04 ARM64 host exercised the complete store → worker →
Unix-socket broker → host path with real Nginx, PHP, WordPress, MariaDB, and Redis.

| Check | Observation |
| --- | --- |
| Static site domain change | An automatically identified site changed domain; replay succeeded. |
| WordPress domain change | Nested serialized URLs changed correctly, while hostname-boundary exclusions, GUIDs, row counts, credentials, Unix identity, and files were preserved. Cached post reads and HTTP responses used the new URL. |
| Pre-activation recovery | An injected failure restored the old database and traffic. |
| Activation retry | An injected activation failure resumed the same job and preserved a database write made after activation began. |
| Partial deletion and replay | An injected late account-removal failure occurred after database/files cleanup. The same job resumed successfully, followed by two successful replays. |
| Deletion scope | The generated WordPress database/user, Unix account, PHP pool, site tree, and panel site row were removed. A shared-PHP neighbor, unrelated SQL database, Redis sentinel, and external symlink target remained intact. |
| Neighbor traffic and integrity | All 259 WordPress/deletion and 13 static-site neighbor samples succeeded. Nginx/PHP syntax checks and SQLite integrity passed. |

These are bounded disposable-host checks, not proof of zero downtime, arbitrary
WordPress integration compatibility, every interruption point, or production
readiness. Public DNS/ACME/TLS transitions, multisite/custom cache integrations,
large databases, sustained load, and power-loss interruption were not exercised.
The disposable host was removed afterwards. Monitoring's local sampling and
failure handling have automated coverage; these checks do not establish
long-term production resource usage.

## 2026-09-06: alpha.6 release and VPS upgrade

Release [v0.1.0-alpha.6](https://github.com/lum1t4/wpx/releases/tag/v0.1.0-alpha.6)
was published at 20:31:52 UTC from source commit
`5ea63d0bb1a291b18c4b34ff7f1acb5871947005`.
GitHub [Verify](https://github.com/lum1t4/wpx/actions/runs/34057998310) passed in
4 minutes 28 seconds and
[Release](https://github.com/lum1t4/wpx/actions/runs/34058041606) passed in
4 minutes 30 seconds. The exact final source passed full race tests, vet, CSS
and bootstrap checks, static Linux amd64/arm64 builds, and attestation
publication. Final local web race tests passed in 163.403 seconds; provision
race tests passed in 7.422 seconds, and vet passed.

At 20:32:15 UTC, the README one-line installation command successfully upgraded
the existing Ubuntu 24.04 amd64 VPS.

| Check | Observation |
| --- | --- |
| Installed release | `wpx version` confirmed alpha.6 and its source commit. |
| Recovery snapshot | Upgrade created `/var/lib/wpx/upgrades/20260906T203215.838873441Z`. |
| Configuration and secrets | All four configuration, encryption-key, and panel TLS file hashes matched their pre-upgrade values. |
| Services and access | All five core services were active; trusted public `/healthz` passed. Port 9443 remained loopback-only behind Nginx on 443, with normal restart logs. |
| Existing WordPress workload | Local HTTP through Nginx with the site's Host header returned 200. |
| Existing site identity | The legacy site's ID, domain, kind, and active status were unchanged. |
| Panel state | SQLite integrity passed, no foreign-key errors were reported, the lifecycle tables were present, and no jobs were pending. |

No domain-change or deletion form was submitted on production. Authenticated
live-browser review of this release remained pending because the workstation
was locked. The successful disposable-host workflows and preview checks above
are separate evidence, not a substitute for that review.

## 2026-09-06: HTTP-01 repair and alpha.8 deployment

The first certificate request after changing the production WordPress domain
failed because Nginx received `EACCES` while reading Certbot's HTTP-01 token.
DNS resolved the new hostname to the expected VPS, and Let's Encrypt reached
port 80, but the intermediate `.well-known` directory retained the privileged
broker's group and mode. Its child challenge directory alone had been assigned
to the site identity. The repair now explicitly reconciles both directories;
a public challenge probe returned 200 before another ACME request was made.

Let's Encrypt then issued the certificate for `ds2.mitadev.com`, valid through
2026-12-05. Its first panel activation exposed stale WordPress options after the
HTTP-to-HTTPS SQL replacement. The final repair invalidates only the immutable
site-ID Redis namespace before option updates and again after all URLs are
final; it never flushes the shared Redis database. Focused provision race tests
and vet passed for each correction.

Release [v0.1.0-alpha.8](https://github.com/lum1t4/wpx/releases/tag/v0.1.0-alpha.8)
was published at 21:03:17 UTC from source commit
`397abab9b8d12728349a0d7c802b36191c0669f1`. GitHub
[Verify](https://github.com/lum1t4/wpx/actions/runs/34059622976) and
[Release](https://github.com/lum1t4/wpx/actions/runs/34059624060) passed; the
release workflow completed in 4 minutes 32 seconds with the full race suite,
vet, UI drift checks, portable builds, and artifact attestations.

The README installer upgraded the VPS and created recovery snapshot
`/var/lib/wpx/upgrades/20260906T210331.428926405Z`. The installed version and
source commit matched alpha.8. The certificate job completed, the panel stored
the site's TLS state as active, both WordPress URL options used HTTPS, and a
trusted request to `https://ds2.mitadev.com/` returned 200. All five core
services were active, SQLite integrity passed, and the site ID/domain/status
were unchanged. Configuration, encryption-key, and panel TLS hashes matched
their pre-upgrade values.

For future entries, record the commit/release, OS and architecture, exact action,
observed result, and untested boundary. A fake-runner unit test, a rendered
preview, and a real provider transaction are different evidence and should
remain distinguishable.
