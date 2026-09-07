# Node.js, FTP, and outbound mail

## Behavior and boundaries

Node.js is a managed companion for a `reverse_proxy` site. The site's upstream
must be `http://127.0.0.1:<runtime port>`. This avoids rebuilding the original
SQLite `sites.kind` CHECK constraint during an upgrade. WPX downloads Node.js
24.20.0 LTS from nodejs.org for amd64 or arm64, verifies the pinned official
SHA-256, and installs it under `/opt/wpx`. A systemd service runs the entrypoint
as the site's Unix identity. The entrypoint must already be a regular file below
the site's `public` directory after resolving symlinks. Arguments are bounded,
quoted for systemd, and never interpreted by a shell. Port availability is
checked immediately before activation; another listener makes the job fail.
The service's systemd address policy permits loopback networking only. This
enforces the reverse-proxy boundary, but applications that require outbound API
or database connections need an explicit future network-policy workflow.

FTP uses ProFTPD virtual users. The browser sends a password once; SQLite keeps
only a cost-12 bcrypt hash and job payloads contain no credential material.
Ubuntu 24.04's libxcrypt and ProFTPD `mod_auth_file` use the bcrypt crypt format.
Each generated passwd entry uses the site's existing uid/gid and public path;
`DefaultRoot ~` confines the session. The managed ProFTPD configuration requires
TLS 1.2 or 1.3 and uses the panel's installed certificate and key. Passive ports
49152-49252 must be opened by the operator if remote FTP is intended.

WPX installs Ubuntu's `proftpd-core` and `proftpd-mod-crypto` packages and loads
`mod_tls` explicitly. The ProFTPD password and configuration files carry the WPX ownership marker;
existing unmarked files and symlinks are refused. The password file is mode
`0600` and explicitly reset to `root:root`. Configuration validation happens
before activation. A validation, start, or reload failure restores both files
and attempts to reload the restored configuration. TLS uses the configured
panel certificate paths. If `mod_tls` is unavailable, the generated fallback
denies login instead of silently accepting plaintext credentials.

Postfix is optional and outbound only. Its durable setup job installs the
package noninteractively, binds SMTP to loopback, trusts only loopback, clears
relay domains, and rejects unauthorized relay destinations. Local applications
may submit to `127.0.0.1:25`; WPX does not create inboxes, accept Internet SMTP,
configure DKIM, or promise delivery reputation. Provider SMTP alerts may use
their separately configured SMTP host instead.

Before installing Postfix, WPX places a temporary `policy-rc.d` guard so the
package cannot briefly start its default listener. An existing regular policy
file is restored after APT and a symlink is refused. WPX refuses to take over an
existing unmarked `main.cf`; managed updates are written, checked, and applied
with a restart so a previously running daemon adopts the loopback binding.

The package install/download operations run in the existing durable worker, so
HTTP requests do not wait for APT or archive extraction. Provisioning converges
managed files. A process loss after host activation but before job completion
can replay the operation; Node's brief port preflight may see its own active
service on replay, so the host integration should stop the marked unit before
testing its requested port when retrying an existing runtime.

Site disable must call `Host.StopSiteHosting(ctx, site)` before reporting the
site disabled. Site enable should reapply the stored runtime when one exists.
Deletion must call `Host.DeleteSiteHosting(ctx, site)` before removing the Unix
identity/site tree; it stops and removes the Node unit and removes every passwd
entry whose home is that site's public path. This prevents a disabled site from
listening and prevents a recreated filesystem path from inheriting FTP access.
These public methods take `h.mu`; lifecycle code that already holds that lock
must call `stopSiteHostingLocked` or `deleteSiteHostingLocked`. Disabling removes
the site's entries from the root-managed ProFTPD password file. Enable replays
the active FTP users from SQLite through `ApplyFTPUser`; deletion purges the
entries permanently before the Unix identity and site tree are removed.

## Shared integration

Append `store.HostingMigration` to `internal/store/migrations.go` after all
existing migrations.

In broker central dispatch, before the unknown-operation response:

```go
if response, handled := s.dispatchHosting(request); handled {
    return response
}
```

`*provision.Host` already implements `broker.HostingOperator`, so no new broad
server field is required. `cmd/wpx/main.go` continues assigning it to
`Server.Provisioner`.

In `worker.ProcessOne`, add:

```go
case "hosting.node_apply", "hosting.ftp_apply", "hosting.mail_apply":
    _, operationErr = w.processHosting(ctx, job)
```

In `store.FinishJob`, after the job completion UPDATE and before commit, add:

```go
if strings.HasPrefix(job.Kind, "hosting.") {
    if err := finishHostingJob(ctx, tx, job, now, operationErr); err != nil {
        return err
    }
}
```

This status update deliberately shares the job completion transaction and does
not overwrite desired state while a newer job for the same target is queued or
running.

In `web.Handler`, call `s.registerHostingRoutes(mux)`. Add these fields to
`pageData`:

```go
NodeRuntime *model.NodeRuntime
FTPUsers    []model.FTPUser
MailService *model.MailService
```

Add `hosting.html` to `preparePageData` with `NavSection = "hosting"`. Site
section inference must recognize `site_runtime.html` as `runtime` and
`site_ftp.html` as `ftp`. Global navigation uses `/hosting` under
`CanManageServer`. Site navigation shows Runtime only for reverse proxies and
FTP for all sites, both under `CanManageSites`.

The activity label map should include `hosting.node_apply` as “Configure Node
runtime”, `hosting.ftp_apply` as “Create FTP account”, and
`hosting.mail_apply` as “Configure outbound mail”.

## Verification

Focused model, store, broker, worker and path-confinement tests cover validation,
credential redaction, persisted-upstream binding, broker rejection, systemd
argument quoting, symlink escape rejection, and auth-file replacement. A full
install test requires a disposable Ubuntu 24.04 systemd host with Internet
access; ordinary unit tests do not claim live APT, ProFTPD TLS, Node, or Postfix
delivery coverage.

The generated service configurations were also checked in a disposable Ubuntu
24.04 container using the distribution packages `proftpd-core` 1.3.8.b,
`proftpd-mod-crypto` 1.3.8.b, and Postfix 3.8.6. Package installation reported
that `policy-rc.d` denied both daemon starts. `proftpd -t` accepted the exact
generated configuration, and `proftpd -vv` reported `mod_tls/2.9.2` and
`mod_auth_file/1.0`. A live loopback plaintext control connection received
`550 SSL/TLS required on the control channel` after `USER`, confirming that the
generated policy does not allow a password before TLS.

`postfix check` accepted the exact generated `main.cf`. Effective `postconf`
values were `inet_interfaces=loopback-only`,
`mynetworks=127.0.0.0/8 [::1]/128`, an empty `relay_domains`, and
`smtpd_relay_restrictions=permit_mynetworks,reject_unauth_destination`. The
container was removed after validation; no host packages or services changed.

On 2026-09-08 a disposable `ubuntu:24.04` container downloaded the pinned
archive, confirmed it against the recorded official amd64 checksum,
reported `v24.20.0` from the installed binary, and served a hello response on
`127.0.0.1`. `systemd-analyze verify` accepted the generated service shape,
including `IPAddressDeny=any`, `IPAddressAllow=localhost`, literal dollar/percent
argument escaping, the working directory, and writable site path. This verifies
archive integrity, runtime execution, loopback binding in the sample, and unit
syntax; it is not a booted-systemd activation test.

Broker-unavailable and lost-response errors requeue hosting jobs through
`Store.RetryHostingJob` with the original idempotency key. The desired-state row
stays queued; it is not falsely marked failed while the privileged operation may
still be completing.
