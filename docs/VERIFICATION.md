# Verification record

This record separates automated coverage, development previews, and real host
observations. Results apply to the candidate and environment described here;
they are not a claim that every supported workflow has been exercised live.

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

## Deployment status

At the time this candidate record was written, the live panel still ran
`v0.1.0-alpha.3` with the domain-access repair. Publishing and deploying the full
candidate remain pending and must be recorded separately after they complete.

For future entries, record the commit/release, OS and architecture, exact action,
observed result, and untested boundary. A fake-runner unit test, a rendered
preview, and a real provider transaction are different evidence and should
remain distinguishable.
