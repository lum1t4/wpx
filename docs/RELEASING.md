# Releasing WPX

GitHub is the release trust authority. There is no project-held offline signing
key. Protect the maintainer account, repository, and workflow because control of
them is control of the release channel.

## Repository controls

Verify these settings in GitHub; their presence cannot be inferred from the
workflow YAML:

- Protect `main` and require verification before merging.
- Restrict creation or replacement of `v*` tags to the maintainer.
- Restrict the `release` environment appropriately.
- Enable immutable releases.
- Protect the maintainer account with two-factor authentication/passkeys.
- Keep third-party Actions pinned to reviewed commit SHAs.

The workflow checks that the tagged commit is reachable from `main`. It does
not configure repository rules, enable immutable releases, or prove the
maintainer account's security settings.

## Prepare and publish

1. Review the candidate diff, generated CSS, operator instructions, and
   requirements coverage. Note fixed issues and known limitations.
2. Check the Verify run for the commit. For installer, service, access, backup,
   or restore changes, record the relevant real-host/provider exercise in
   addition to unit tests.
3. Choose a new version; never move an existing release tag to repair a release.
   Create an annotated `v*` tag on the tested commit and push it.
4. Watch the Release workflow, then inspect the published assets and metadata.
   Fix a failed release through a reviewed change and new version when needed.

The release workflow runs on `v*` tag pushes. It rebuilds Tailwind and rejects
generated CSS drift, checks shell syntax and styling rules, runs race tests and
vet, then cross-builds static amd64 and arm64 Linux binaries. Build metadata
comes from the tag and commit timestamp.

It publishes:

- `wpx-linux-amd64` and `wpx-linux-arm64`;
- `checksums.txt` for those binaries;
- `install.sh`;
- `go-modules.json`, the Go module inventory.

GitHub Actions attaches provenance attestations to the binaries, checksum
manifest, and installer. The module inventory is not a full inventory of host
packages or every bundled dependency. `gh release create` creates a new release;
the workflow does not overwrite an existing one. GitHub's immutable-release
setting provides the platform-level protection.

## Verify the release result

With `gh` authenticated and the repository selected:

```sh
gh run list --workflow release.yml --limit 5
gh release view --repo lum1t4/wpx
```

Inspect the run for the intended tag/commit and download that release's assets
to a temporary directory. Check SHA-256 values and, when independently checking
build provenance, use GitHub's `gh attestation verify` for each binary with
`--repo lum1t4/wpx`. Verify `wpx version` on the matching Linux architecture
and exercise the bootstrap on a disposable host.

The one-line installer trusts the HTTPS GitHub release channel and matches the
downloaded binary to its checksum. It does **not** run attestation verification;
a checksum from the same compromised source would not independently establish
authenticity. Do not call the bootstrap a signature verifier.

## Installation, resume, and upgrade

A fresh installation records `/var/lib/wpx/.installing`. Recognized interrupted
installs resume; unrelated existing web/database state remains a conflict.
With an existing config, resume preserves the access address, update preference,
secret key, and TLS material. It refuses missing/invalid original secret files.
Only the bootstrap token is regenerated, since its old plaintext is not stored.
Existing owner setup remains closed.

Resume restarts the broker and panel so they load current files. Successful
installation requires the local HTTPS/SQLite health check and active managed
core services before removing the marker. These checks do not validate public
DNS or external firewall rules.

On a completed installation the bootstrap invokes `upgrade` from the downloaded
binary. Upgrade stops the panel and broker, checkpoints SQLite, snapshots the
old binary/state/WPX systemd units under `/var/lib/wpx/upgrades`, installs the
candidate, reconciles units, and checks local panel/broker readiness.

Failure triggers a recovery attempt using the snapshots. The database is
replaced only after both services stop; stale SQLite WAL/SHM files are removed
at that boundary. Previous units are restored, systemd is reloaded, and the old
services are started. A successful restart does not itself mean the old HTTP
endpoint was re-probed. Recovery failures retain the directory and report the
failed step. Configuration, packages, hosted-site data, and remote provider
changes are not a complete transactional rollback set.

Release checks are optional and announce versions only. The operator explicitly
runs the install command to upgrade. The bootstrap's temporary download is
removed when it exits. See [Operating WPX](OPERATIONS.md) for diagnostics and
operator recovery expectations.
