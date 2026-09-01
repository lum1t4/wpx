# Product specification

## Audience

WPX initially serves technical freelancers and small agencies operating roughly
five to fifty sites on their own VPS. The product is local-only: each panel
manages the server on which it runs.

## Certified environment

- Ubuntu 24.04
- Linux amd64 and arm64
- Fresh server installation
- Nginx, MariaDB, Redis, PHP-FPM, Python, nftables, and systemd

## Site types

- WordPress
- Generic PHP
- Python
- Static
- Reverse proxy

Node.js is intentionally postponed.

## Roles

- Owner: ownership, security policy, updates, users, server, and every site.
- Administrator: server and all-site operation without ownership transfer.
- Collaborator: capability-based access to assigned sites.
- Customer: a curated subset of operations on assigned sites.

## WordPress experience

WordPress sites provide administrator magic login, core/plugin/theme inventory,
activation and updates, protected pre-update backups, health checks, rollback,
Redis object cache, FastCGI cache, multisite support, and recovery operations.

Magic login selects an active administrator and creates a short-lived,
single-use token. It never creates a hidden permanent user or changes a password.

## Staging

The ordinary interface has four actions: Create staging, Open WordPress, Sync
from production, and Deploy to production. Creation clones files and database,
performs serialized-safe URL replacement, configures SSL, enables password and
search-engine protection, and suppresses outgoing side effects.

Deploy offers SiteGround-style Full deploy and Custom deploy. Custom deploy can
select changed files and database tables. Every deploy creates a protected live
recovery point and runs health checks. WPX does not describe table replacement
as a conflict-free database merge.

## Backups

Backups are encrypted, incremental, integrity-checked, and portable without WPX.
Targets initially include S3-compatible object storage with custom endpoints and
Google Drive. Restore supports original site, new site, and staging. Scheduled
restore tests are optional.

## DNS

Cloudflare and AWS Route 53 integrations manage only records explicitly owned
by WPX. They support ordinary records, staging records, DNS-01 certificates,
wildcards, and Cloudflare proxy state.

## Runtime versions

PHP 7.1 through 8.5 may be installed on demand. End-of-life versions require an
explicit advanced-mode confirmation and are never selected by default. An FPM
service is stopped when no enabled site uses that version.

## Administration

The panel supports password authentication and TOTP. It may listen publicly,
remain loopback-only, be served on its own trusted domain and certificate, or be
reached through Tailscale and its ACLs. There is no browser terminal. The file
manager can edit PHP as the site user with revisions, syntax validation, atomic
saves, and path confinement. Experts may edit validated Nginx and PHP snippets.

Owners can create, edit, disable, and re-enable accounts. Administrators can
manage non-owner accounts but cannot grant or alter administrator access.
Disabling an account or changing its password revokes its active sessions and
pending login challenges immediately. Collaborator and customer access is
limited to explicitly assigned sites.

The Activity view shows durable work and actionable failures without exposing
job payloads, credentials, or results. Owners and administrators see server-wide
activity; other roles see only work for sites they can access. Metrics, capped
logs, and audit history remain on the managed server and are never transmitted
to the WPX project.

Sites can be disabled without deleting their files, databases, certificates, or
backups. WPX removes traffic and the workload activation, stops an unused PHP
version, and can later converge the site back to its active configuration. A
failed provision or lifecycle action remains visible and can be retried.

## Updates and distribution

WPX is distributed as static `linux-amd64` and `linux-arm64` binaries. GitHub is
the release trust authority. GitHub Actions creates build provenance and
artifact attestations; there is no project-held release private key. Update
checks are automatic and installation is manual in the first stable release.
Existing releases are immutable.

Running the installer on an existing host performs an explicit upgrade. WPX
captures the installed binary, configuration, and SQLite state, reconciles its
systemd units and runtime permissions, verifies service health, and restores the
snapshot automatically if the upgrade fails.

The primary installation experience is one line. A small bootstrap downloads the
architecture-specific binary and checksum from GitHub, then the binary performs
the versioned installation. There is no APT repository or `.deb` package for WPX.
