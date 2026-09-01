# WPX

WPX is a small, open-source server panel for WordPress, PHP, Python, static,
and reverse-proxy sites. It is designed for agencies and independent operators
who want a production hosting workflow without handing control of their server
or operational data to a hosted control plane.

The first certified platform is Ubuntu 24.04 on Linux amd64 and arm64. WPX is
distributed as a static Go binary; platform-specific package and service logic
is kept behind adapters so other Linux distributions can be added later.

## Status

WPX is an alpha release and is not yet recommended for production servers. The
main product workflows are implemented and exercised on disposable Ubuntu 24.04
systemd hosts, but the project has not yet accumulated the release history and
real-world operating time required for a production recommendation. Install it
only on a fresh test VPS whose contents can be recreated.

## Install

On a fresh Ubuntu 24.04 VPS:

```sh
curl -fsSL https://github.com/lum1t4/wpx/releases/latest/download/install.sh | sudo sh
```

The daily GitHub release check sends only a generic `WPX-update-check` user
agent and is optional. Disable it during installation with:

```sh
curl -fsSL https://github.com/lum1t4/wpx/releases/latest/download/install.sh | sudo sh -s -- --no-update-checks
```

On an installed host, use `sudo wpx updates --disable` or
`sudo wpx updates --enable`. `wpx updates --status` is read-only.

If installation is interrupted before WPX prints the setup URL, rerun the same
one-line command. WPX records an in-progress installation marker and resumes
idempotently; it still refuses to take ownership of an unrelated existing web
stack.

The bootstrap selects `amd64` or `arm64`, downloads the static release binary
and its checksum from the same GitHub release, verifies it, and then hands all
host changes to the versioned Go installer. WPX has no APT repository and does
not publish a `.deb`; Ubuntu packages are installed only for the web-server,
database, language runtimes, and other host services it manages. Until the
repository name is final, forks can set `WPX_REPOSITORY=owner/repository`.

## Design commitments

- The public web process never runs as root.
- Privileged work crosses a typed Unix-socket protocol; arbitrary shell strings
  are not part of that protocol.
- Site workloads run as dedicated site users.
- Telemetry is never sent to the WPX project. Metrics and logs stay local.
- The release is a static Linux binary, not a Debian package.
- The convenient installer is one line; installation logic lives in the signed
  and attested binary rather than a large mutable shell script.
- Running that same command on an existing WPX host performs an explicit
  upgrade with a local binary and SQLite snapshot, health check, and automatic
  rollback on failure.
- Destructive workflows create recovery points and durable audit events.

Read [the architecture](docs/ARCHITECTURE.md), [product specification](docs/PRODUCT.md),
and [engineering style](docs/STYLE.md) before contributing.

## Panel access

The installer initially exposes the self-signed panel on port 9443 so the owner
can complete setup. Afterwards, run exactly one root-authorized access mode:

```sh
sudo wpx access --domain panel.example.com
sudo wpx access --tailscale
sudo wpx access --local
sudo wpx access --public
```

Domain mode obtains a Let's Encrypt certificate, publishes the panel through a
WPX-owned Nginx virtual host, and moves the application listener to loopback.
Tailscale mode configures private HTTPS with Tailscale Serve and also moves the
listener to loopback. It expects an already installed, connected Tailscale node;
WPX never handles tailnet enrollment credentials. Public mode deliberately
restores the direct `0.0.0.0:9443` listener and its local certificate.

Access changes refuse to overwrite unmanaged Nginx files. If the panel restart
fails, WPX restores the previous configuration and attempts to restart it.

## Development

Go is not required on the host when Docker is available:

```sh
docker run --rm -v "$PWD:/src" -w /src golang:1.26-bookworm go test ./...
```

Build portable Linux binaries:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o dist/wpx-linux-amd64 ./cmd/wpx
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o dist/wpx-linux-arm64 ./cmd/wpx
```
