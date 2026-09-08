# Integration verification — 2026-09-08

This section records pre-release integration checks in the local WPX checkout.
Release publication and subsequent VPS upgrades are recorded in
[the deployment verification record](../VERIFICATION.md). No real SMTP delivery
was performed during these integration checks.

## Automated checks

Passed: `go test ./...`, `go test -race ./...`, `go vet ./...`, both Linux
release builds, JavaScript syntax checks, `sh -n install.sh`, and
`git diff --check`. The final worker additions also passed focused race tests.

The integration checks cover schema upgrades from the pre-feature database,
existing site/job/secret preservation, authorization and CSRF, durable worker
completion and uncertain-outcome replay, site lifecycle reservations, and
filesystem confinement. Feature suites additionally exercise upload replay,
archive traversal, cron validation, access gates, SMTP incident handling,
WordPress fleet update selection, and hosting configuration rollback.

Linux Go 1.26 is used because the host implementation is Linux-specific. Release
builds use CGO_ENABLED=0 for both linux/amd64 and linux/arm64. Tailwind is rebuilt
from source and browser scripts receive JavaScript syntax checks.

## Real service checks

Disposable Ubuntu 24.04 containers verified generated configuration with:

- Nginx 1.24.0: HTTP/TLS site configurations, Cloudflare-origin rejection, Basic
  authentication, staging and public ACME challenges. Live HTTP probes returned
  the expected 403/401 and ACME 200 responses.
- Fail2ban 1.0.2 and nftables 1.0.9: configuration parsing and action expansion.
  Exact log fixtures matched login 1, XML-RPC 1, sensitive paths 2 and 404 bursts
  4, while the intended false positives were excluded. No firewall ban was
  executed.
- ProFTPD 1.3.8.b: generated configuration, TLS/auth modules and a live plaintext
  login rejection. Postfix 3.8.6 accepted the generated configuration and exposed
  the intended loopback-only policy. Package startup was suppressed during
  installation.
- Node.js 24.20.0: the pinned official amd64 archive checksum, executable version,
  a loopback hello server, and systemd unit syntax. A booted systemd activation
  and the arm64 runtime archive execution were not tested.

## Browser review

The local read-only preview was checked for database layout, dedicated site
navigation, file controls and multi-selection, and security layout. An isolated
browser fixture verifies response-confirmed partial page updates, visible
content after animation and focus restoration. Server/API tests cover mutations
that the preview deliberately does not execute.

## Product boundaries

File operations have explicit size, depth, entry and time limits documented in
[files_manager.md](files_manager.md). Large uploads resume, but large recursive
archives/copies must be narrowed. Mail is local outbound submission, not mailbox
hosting. Node's default service networking allows loopback only, including its
outbound connections. Cloudflare-only sites cannot enable the current
origin-address firewall defenses. The 33 locale catalogs translate common
static interface labels; longer copy and backend errors retain English fallback.
