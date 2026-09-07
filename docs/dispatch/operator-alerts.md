# Operator SMTP alerts

Operator alerts run in the unprivileged web process and share its lifetime. The
monitor reads the existing in-memory resource sampler once per minute. It asks
the root broker for a bounded snapshot of fixed core services, validated managed
runtime services, recent kernel OOM messages, and certificate expiry. Browser
requests only read or save settings; opening the page never samples the host or
sends mail.

SMTP host, username, password, sender, recipient, TLS mode, thresholds, and
switches are serialized together and encrypted with the existing application
secret key. The settings page receives a redacted copy and a separate boolean
that says whether a password exists. Saving an empty password keeps the stored
password. Both implicit TLS and STARTTLS require TLS 1.2 or newer and verify the
configured server name. The test action sends only after an authenticated,
CSRF-protected owner/administrator POST; automated and unit tests inject a fake
sender and do not deliver real mail.

CPU, memory, and disk conditions need three consecutive bad readings. Every
condition needs two healthy readings to clear. State is persisted in SQLite so
a panel restart cannot resend an active incident. OOM messages are fingerprinted
and a persisted cooldown limits distinct events. Alert bodies include at most an
8 KiB resource/diagnostic snapshot; service journals are limited to 20 lines and
4 KiB each, kernel inspection is limited to 40 lines, and only 10 matching OOM
lines survive. SMTP credentials never enter logs, UI values, diagnostics, alert
state, or audit details.

Managed swap is disabled by default and requires its own explicit checkbox. If
enabled while the kernel reports no swap, WPX creates one fixed 2 GiB file at
`<data-root>/swap/wpx.swap`. The broker accepts only aligned sizes from 512 MiB
through 8 GiB, rejects symlinked/unmanaged paths, serializes the host change,
keeps a 1 GiB filesystem reserve, and uses a WPX ownership sidecar. A restart
rechecks and activates the same file rather than creating another one. A failure
before activation removes the incomplete swap file; the ownership marker can
remain for a safe retry.

## Shared integration

The root integration uses these exact additions:

```go
// internal/store/migrations.go, after existing release migrations
OperatorAlertsMigration,

// internal/broker/server.go, before the central operation switch
for _, extension := range []func(Request) (Response, bool){
    s.dispatchOperatorAlerts,
} {
    if response, handled := extension(request); handled { return response }
}

// internal/web.Server
alerts *alerts.Monitor

// web.New, after constructing resources
alerts: alerts.New(state, resources, privileged, logger),

// ListenAndServe, using the same cancellable monitor context
go s.MonitorOperatorAlerts(monitorCtx)

// Handler
s.registerOperatorAlertRoutes(mux)

// pageData
OperatorAlerts *operatorAlertsView

// preparePageData
case "alerts.html": data.NavSection = "alerts"
```

The global server navigation links to `GET /alerts` when `CanManageServer` is
true and uses `NavSection == "alerts"` for its selected state. The feature route
registrar also owns `POST /alerts` and `POST /alerts/test` and applies the same
`ManageServer` capability to every route.

The broker operation constants live in `internal/broker/operator_alerts.go`:
`OpOperatorDiagnostics` and `OpOperatorEnsureSwap`. The central dispatcher calls
`s.dispatchOperatorAlerts`. The helper asserts the narrow diagnostics interface
on `s.Provisioner`; `*provision.Host` implements it with its existing runner and
configured roots, so no broad interface or broker server field changes are
needed.

The SQLite transaction is the commit boundary for settings. Host observations
and SMTP delivery happen later and cannot be rolled back with that transaction.
An SMTP failure leaves the incident unsent and eligible for retry. A successful
SMTP delivery followed by failure to persist state can cause one duplicate on a
later check; SMTP has no transactional receipt protocol. Swap activation is a
host boundary: if `swapon` succeeds but the broker reply is lost, the next call
detects the active managed path and reports it without recreating it.
