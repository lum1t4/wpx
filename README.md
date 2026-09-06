# WPX

WPX is a free, GPL-3.0 server panel written in Go. It manages WordPress, PHP,
Python, static, and reverse-proxy sites on one server, with local accounts,
site permissions, staging, backups, database tools, DNS tools, and local resource monitoring.
There is no hosted WPX account or product telemetry.

The interface keeps server administration separate from work on an individual
site. It uses server-rendered HTML and direct Tailwind utilities, with a quiet,
neutral appearance inspired by shadcn/ui.

## Status and platform

WPX is alpha software. Its source and tests are available, but implemented
features are not a production reliability guarantee. Use a fresh test server
whose contents can be recovered while evaluating it. See the
[requirements and current limits](docs/PRODUCT.md) before moving a workload.

Installation currently supports **Ubuntu 24.04, amd64 and arm64**. The WPX binary
is statically linked and needs no Go or Node.js runtime on the server. Nginx,
MariaDB, Redis, PHP, and Python still have operating-system dependencies; other
Linux distributions are not supported yet. WPX has no APT repository or `.deb`
package. It uses Ubuntu packages and a PHP package source for managed runtimes.

## Install

On a fresh Ubuntu 24.04 VPS, run:

```sh
curl -fsSL https://github.com/lum1t4/wpx/releases/latest/download/install.sh | sudo sh
```

Open the HTTPS setup address printed by the installer and use its one-time
bootstrap token to create the owner account. The initial certificate is
self-signed. Then configure a panel domain or private access and enable TOTP
under Account. The [operator guide](docs/OPERATIONS.md) explains these steps,
required ports, and recovery diagnostics.

The same command resumes a recognized interrupted installation, or upgrades a
completed WPX installation. It refuses an unrelated existing hosting stack.
An upgrade briefly stops the panel and broker and keeps local recovery files.
It does not replace your operating-system backup.

Daily GitHub release checks are optional. To disable them on first installation:

```sh
curl -fsSL https://github.com/lum1t4/wpx/releases/latest/download/install.sh | sudo sh -s -- --no-update-checks
```

For an installed panel:

```sh
sudo wpx updates --disable
sudo wpx updates --status
```

Checks announce releases; installing one remains an explicit operator action.
The bootstrap verifies the binary's SHA-256 checksum from the same GitHub
release. GitHub Actions publishes provenance attestations; the installer does
not independently verify those attestations or use a project signing key.

## Find your way around

Server navigation contains Overview, Sites, Activity, Monitoring, Databases,
Storage, DNS providers, Users, and Account. Server resources such as storage credentials are configured
once. Inside a site, Overview, WordPress, Staging, Backups, SSL & security,
Settings, Files, DNS, and Logs expose the tools relevant to that site and your role.

Start with Sites → create a site. Connect storage before using protected
WordPress updates, restores, or staging deployment. Activity shows whether a
requested operation is waiting, running, complete, or failed.

Name sites by their domain; WPX assigns an internal UUID to new sites and copies.
Settings lets an owner or administrator change a site's domain or PHP version.
The owner can also permanently delete a site with explicit domain confirmation.
Monitoring shows current server resources and up to an hour of history held
only in memory. See the operator guide for each action's scope and recovery limits.

## Documentation

- [Operating WPX](docs/OPERATIONS.md): setup, access, backups, upgrades, diagnostics.
- [Product and requirements](docs/PRODUCT.md): what is implemented and what remains.
- [Architecture](docs/ARCHITECTURE.md): source map, identities, state, failure boundaries.
- [Contributing and testing](docs/DEVELOPMENT.md): toolchain, CSS, tests, host verification.
- [Verification record](docs/VERIFICATION.md): dated checks and their limits.
- [Engineering style](docs/STYLE.md): code comments, errors, tests, utility-first UI.
- [Releases](docs/RELEASING.md): maintainer workflow and release trust.

WPX is licensed under [GPL-3.0](LICENSE). The project name is provisional.
