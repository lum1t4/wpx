# Quick action responses

`internal/web/handlers_actions.go` adds a progressive enhancement for short
durable jobs. A normal form submission retains the existing 303 redirect. A
form marked `data-quick-action` is submitted by `static/actions.js` with
`X-WPX-Action: partial`; its handler passes the returned job ID to
`respondQueuedAction`.

The helper waits for at most 900 milliseconds and reads durable job state. It
returns `succeeded` only after the worker stored completion, `failed` with the
stored operation error, or `queued`/`running` with an Activity link. The browser
polls the authenticated status route for at most twelve seconds. Confirmed
completion refreshes `#main-content` from the handler's return URL and fades it
in. A longer job stays visibly in progress and remains available in Activity.

Use this only for expected short work: additional database create/delete, site
enable/disable, expert configuration, and WordPress performance. Site install,
site deletion, domain changes, phpMyAdmin install, backup, restore, staging, and
WordPress update remain background workflows with their existing redirects.

## Integration

In `Server.Handler`, after creating `mux`, register the polling route:

```go
s.registerActionsRoutes(mux)
```

In `templates/base.html`, include the embedded script before `</body>`:

```html
<script src="/assets/actions.js" defer></script>
```

For an eligible handler, retain the job ID and replace its final redirect with:

```go
s.respondQueuedAction(w, r, user, jobID, returnURL, "Configuration")
```

Mark only the matching form, with concise user-facing copy:

```html
<form method="post" data-quick-action data-action-label="Saving configuration" ...>
```

The polling lookup authorizes administrators, the job initiator, or a user who
can still view the target site. It returns no payload or result JSON. A supplied
return URL must be local; unsafe values fall back to `/jobs`.
