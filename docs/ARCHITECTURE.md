# Architecture

This document records the boundaries that must remain true as WPX grows. The
comments in the code explain local mechanisms; this document explains why the
system is divided this way.

## Processes and identities

WPX ships as one static executable but runs it in two different modes:

```text
browser
  -> wpx serve          (Unix user: wpx)
       -> SQLite        (/var/lib/wpx/state.db)
       -> Unix socket   (/run/wpx/broker.sock)
            -> wpx broker       (Unix user: root)
                 -> validated filesystem changes
                 -> package/service adapters
                 -> dedicated site users
```

Using one executable keeps installation and upgrades simple. Using separate
processes prevents a web-handler defect from automatically gaining root
authority. Broker mode must initialize only the code necessary for privileged
operations: it does not expose TCP, render templates, or accept shell programs.

The socket is owned by `root:wpx` with mode `0660`. The broker also checks the
peer credentials supplied by the Linux kernel. File permissions are a first
boundary, not the only validation boundary.

## Typed privileged operations

The broker protocol accepts an operation name and a versioned JSON payload.
Every operation validates identifiers, derives paths from trusted roots, and
executes programs with argument arrays. It never accepts an executable name,
shell string, absolute target path, or systemd unit name from the web process.

Operations are designed to be idempotent. A request carries an idempotency key;
the final implementation persists outcomes so retrying after a lost response
does not duplicate destructive effects.

## State

SQLite is the source of truth for panel identities, desired site state, jobs,
and audit events. Generated Nginx and PHP configuration is output state and can
be reconstructed. Site content and application databases are external managed
state and are never hidden inside the panel database.

Migrations run in transactions. A new binary must remain compatible with the
previous schema until its health check succeeds, otherwise update rollback must
restore the pre-update database snapshot.

## Platform adapters

Core workflows depend on a narrow platform interface: detect platform, install
packages, manage services, and report canonical paths. Ubuntu 24.04 with systemd
is the first adapter. Supporting another distribution means implementing and
testing an adapter, not adding distribution checks throughout product code.

## Jobs

Provisioning, backups, restores, staging, deployments, updates, and certificates
are durable jobs. Each job records phases, progress, initiator, recovery point,
and final status when those details exist for that operation. WPX does not
invent percentage estimates that an underlying tool cannot report reliably.
HTTP requests enqueue work; they do not hold connections open while operating
on sites.

## Network policy

WPX sends no product telemetry. Functional egress such as certificate issuance,
DNS APIs, backup storage, Google Drive, and GitHub update checks is catalogued in
the UI. Local metrics and logs have explicit retention and disk caps.
