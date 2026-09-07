# Localization and cloud metadata

## Localization boundary

`internal/localize` supports 33 locales. Every locale has native selector text
and 97 translated navigation, feature, action, status, form-field, and file
manager labels.
The catalog includes the recurring file operations, resource states, account
fields, mail fields, CPU/memory/disk labels, and certificate expiry vocabulary
used by WPX. `T` explicitly falls back to
the English catalog when a future locale entry is missing.

The locale is request-local. `localize.Middleware` wraps the response writer
with `localize.RequestWriter`, which exposes the selected locale and current
relative URL to the renderer. The wrapper implements `Unwrap`, so
`http.ResponseController` can still reach streaming and proxy capabilities of
the underlying writer. Selection order is the secure `wpx_locale` cookie, the
browser's `Accept-Language`, then English. `GET /language` validates the chosen
locale, writes a one-year Secure, HttpOnly, SameSite=Lax cookie, and accepts only
a same-origin absolute-path redirect target.

Templates mark a static leaf element with `data-i18n="catalog_key"`. After the
template has rendered into its existing buffer, `TranslateHTML` changes only the
plain text of those marked leaf elements. Marked containers with child markup
are ignored. Script, style, textarea, pre, and code regions are never inspected.
Do not mark elements containing domains, usernames, credentials, file contents,
logs, or other generated/user content.

Root integration uses these page fields:

```go
Locale      localize.Locale
Languages   []localize.Option
CurrentPath string
```

In `renderStatus`, populate them from a `localize.RequestWriter`, render, then
write `localize.TranslateHTML(body.Bytes(), data.Locale)`. Wrap the final web
handler in `localize.Middleware` and register `GET /language` with
`localize.SwitchHandler`.

## Cloud metadata boundary

`internal/cloudmeta.Detect(ctx)` supports AWS EC2, Google Compute Engine,
DigitalOcean, Hetzner Cloud, and Vultr. It uses fixed HTTP paths on
`169.254.169.254:80`; the production dialer rejects every other destination,
does not use environment proxies, does not follow redirects, limits scalar
bodies to 128 bytes and Vultr's JSON document to 64 KiB, and bounds requests to
350 ms with a 1.6 second overall deadline.
Responses must be public IPv4 addresses. AWS requires an IMDSv2 token. GCE
requires the `Metadata-Flavor: Google` request and response headers. Vultr sends
its required metadata token header. Missing or disabled metadata returns an
empty result without making a public-network IP discovery request.

Detection should not block construction or a page request. Add a
`*cloudmeta.Cache` to the web server, initialize it with `cloudmeta.NewCache()`,
and call `Start` once from `ListenAndServe` with the service lifetime context.
`Result()` is non-blocking and returns `(result, ready)`. A server overview can
show `result.Provider` and `result.PublicIP` only when ready and non-empty; an
empty result is the normal non-cloud/private-only fallback.

The fixed endpoints and required headers were checked against provider-owned
documentation:

- [AWS EC2 instance metadata IPv4](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/working-with-ip-addresses.html)
  documents the IMDSv2 token flow and `latest/meta-data/public-ipv4`.
- [Google Compute Engine metadata](https://cloud.google.com/compute/docs/metadata/querying-metadata)
  documents the link-local endpoint and mandatory `Metadata-Flavor: Google`.
- [DigitalOcean network-interface metadata](https://docs.digitalocean.com/reference/api/metadata/network-interfaces/)
  documents `metadata/v1/interfaces/public/0/ipv4/address`.
- [Hetzner Cloud metadata](https://docs.hetzner.cloud/reference/cloud#metadata)
  documents `hetzner/v1/metadata/public-ipv4`.
- [Canonical cloud-init's Vultr datasource](https://github.com/canonical/cloud-init/blob/main/cloudinit/sources/helpers/vultr.py)
  is authored in part by Vultr and reads `v1.json` with
  `Metadata-Token: cloudinit`. WPX reads the primary interface's IPv4 address
  from the same bounded JSON document and treats an absent or changed response
  as a normal no-detection result.
