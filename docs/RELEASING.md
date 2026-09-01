# Releasing WPX

GitHub is the release trust authority. WPX does not maintain an offline signing
key or a separate release-key ceremony.

## Repository controls

Before the first release, configure GitHub so that:

- `main` is protected and changes require the verification workflow;
- `v*` tags can be created or changed only by the owner;
- the `release` environment is owner-restricted;
- immutable releases are enabled for the repository;
- two-factor authentication and a hardware-backed passkey protect the owner;
- Actions are limited to the pinned actions used by this repository.

These settings are part of the security model. The workflow cannot compensate
for a repository that allows tags or existing release assets to be rewritten.

## Release flow

1. Confirm `main` is green and update operator-visible release notes.
2. Create an annotated `v*` tag on a commit reachable from `main`, then push it.
3. The release workflow rebuilds Tailwind, rejects generated CSS drift, runs the
   race-enabled tests and static analysis, and cross-builds static Linux binaries
   for amd64 and arm64.
4. The workflow derives build metadata from the immutable tag commit, generates
   checksums and a Go module inventory, and asks GitHub to attest the binaries,
   checksum manifest, and bootstrap.
5. The workflow creates one immutable GitHub release. It never updates an
   existing release.

Operators install manually from the release-hosted `install.sh`. Automatic
update checks may announce a newer immutable release, but stable WPX does not
replace its own running binary without an explicit operator action.

The bootstrap is also the upgrade entry point. If it finds the installed binary
and configuration, the verified download invokes `wpx upgrade`. WPX stops the
services, checkpoints SQLite, snapshots the current binary and database under
`/var/lib/wpx/upgrades`, installs the release, starts the broker before the
panel, and checks `/healthz`. A failed start or health check restores both
snapshots. The temporary verified download is removed when the command exits.

Fresh installation writes `/var/lib/wpx/.installing` before the first host
mutation and removes it only after every service is healthy. The bootstrap uses
that marker to resume interrupted installation rather than invoking upgrade.
The Go installer validates the marker contents and continues to reject
unrelated pre-existing web-server or database state.
