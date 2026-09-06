# Contributing and testing

Work from the repository root. Read [the architecture](ARCHITECTURE.md) for the
state/privilege boundaries and [engineering style](STYLE.md) before changing a
host operation. Source is GPL-3.0; preserve compatible license notices when
reusing code.

## Toolchain and ordinary checks

`go.mod` declares Go 1.26.0. CI uses Node.js 24 and the Tailwind versions locked
in `package-lock.json`. Node and Go are development tools, not server runtime
requirements.

```sh
npm ci --ignore-scripts
npm run build:css
go test ./...
go vet ./...
```

Before submitting changes that affect concurrency or Linux host behavior, run
the CI checks on Linux:

```sh
go test -race ./...
sh -n install.sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o dist/wpx-linux-amd64 ./cmd/wpx
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o dist/wpx-linux-arm64 ./cmd/wpx
```

The static release uses `CGO_ENABLED=0`; race-enabled tests need the normal
compiler/CGO environment. Passing a macOS test run does not exercise files with
Linux build tags, including the file editor and broker peer credentials.

When Go is not installed locally, use the Docker toolchain:

```sh
docker run --rm -v "$PWD:/src" -w /src golang:1.26-bookworm go test ./...
docker run --rm -v "$PWD:/src" -w /src golang:1.26-bookworm go test -race ./...
docker run --rm -v "$PWD:/src" -w /src golang:1.26-bookworm go vet ./...
```

Docker Desktop must be running. These commands compile and run tests; they do
not install WPX or prove that real services, package downloads, DNS, or storage
providers work. On native Linux, container writes to a bind mount may be owned
by root; use an appropriate container UID/cache setup for your environment.

## UI changes

Preview the real templates with disposable sample data, without installing the
panel or connecting to a host:

```sh
go run ./cmd/wpx-preview
```

Open `http://127.0.0.1:9080`. This separate development command seeds temporary
SQLite data, supplies a synthetic session and broker responses, and labels the
pages as read-only. GET/HEAD navigation works; form submissions are rejected.
It does not start the worker, read an installed configuration, or call providers.
Ctrl+C shuts it down and removes its temporary state.

The equivalent Docker preview keeps the published port on host loopback:

```sh
docker run --rm -it -p 127.0.0.1:9080:9080 -v "$PWD:/src" -w /src golang:1.26-bookworm go run ./cmd/wpx-preview -listen 0.0.0.0:9080
```

The preview is for navigation, layout, and empty/sample states. It is not a test
of authentication or the effects of a submitted form. Use HTTP tests and a
disposable installed host for those behaviors. Never deploy the preview as a
public panel.

Templates live in `internal/web/templates`; the stylesheet entry point is
`assets/input.css`. Compiled `internal/web/static/app.css` is tracked and embedded
in the Go binary. Rebuild it after changing Tailwind utilities or design tokens
and include the generated diff. CI rebuilds and rejects drift.

Keep server navigation separate from site navigation. A section should expose
one coherent task and load only the data it needs: a WordPress inventory failure
must not make Files or SSL unusable. Shared templates may express repeated
navigation/form behavior; keep the actual Tailwind utilities in the markup.
Do not introduce BEM, `@apply`, semantic CSS aliases, or gradients for decoration.

Verify the interaction, not only a template snapshot. Check keyboard focus,
labels, empty/error states, narrow screens, current-section navigation, and
whether destructive actions explain the affected site. Hidden buttons are not
authorization; handlers must reject the same operation for an unauthorized
account. See `internal/web/*_test.go` for session, role, navigation, and form-state
test fixtures.

## Host verification

Installation writes system configuration and installs packages. Run it only in
a disposable Ubuntu 24.04 systemd environment or a fresh test VPS. A normal
`ubuntu:24.04` container does not boot systemd; it is sufficient for a dry-run
platform check, not a complete install test. A systemd container usually needs
additional capabilities and cgroup access, so isolate it from production hosts
and credentials.

For a tested candidate, record the commit, OS/architecture, environment, exact
workflow, and result in the PR/release notes. A useful host exercise is:

1. Install from fresh state; verify setup, login, service restart, and reboot.
2. Provision a static site and WordPress site; request their pages through Nginx.
3. Create a second account and verify both permitted and forbidden site access.
4. Test panel domain/private access changes, including a failed certificate step.
5. For backup/staging changes, use disposable data and actual configured storage;
   restore it and compare the recovered files/database.
6. Upgrade from the preceding release and exercise a forced readiness failure
   with recoverable fixtures. Check the old binary/state can restart.

Not every change needs the entire exercise. Pick checks that can expose its
failure mode. Unit tests with a fake command runner validate ordering and
arguments, but they cannot establish that apt, Nginx, PHP-FPM, or a provider
accepts those arguments on a real host.

## Change an operation without losing its recovery story

Start with the invariant. For example, a restore must retain recoverable live
data until the imported copy passes checks. Locate the store transaction, worker
dispatch, broker validation, and host mutation before changing any one layer.

Make comments explain who owns a path, what has committed before an error,
whether retry reuses an existing result, and which temporary/recovery files may
remain. Put these explanations beside the ordering they justify. Test the
boundary that could violate the invariant, including a lost response or partial
failure when relevant; avoid tests that only repeat implementation details.

Keep errors contextual and actionable while preserving wrapped causes. Do not
put credentials in errors, logs, job summaries, URLs, or fixtures copied from a
real installation. Use temporary roots and fake runners for package/service
tests. Never let a unit test reach the developer's actual `/etc` or a production
provider account.

## Review and release evidence

The Verify workflow rebuilds CSS, enforces the utility-first convention, checks
shell syntax, runs race tests/vet, and cross-builds both Linux architectures.
It does not run a full systemd installation or live cloud integration test.

Describe what changed, why, and what was actually exercised. Mark a feature as
implemented separately from provider/host verification. Update the relevant
operator instructions and [requirements coverage](PRODUCT.md) whenever behavior
or limitations change. Follow [the release guide](RELEASING.md) for publishing.
