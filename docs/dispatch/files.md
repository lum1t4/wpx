# File manager integration

The file manager keeps the text editor and adds multi-select file operations,
site-wide search, streamed download, and resumable chunked upload. All browser
paths remain relative to the selected site's public root. The web process loads
and authorizes the site for every endpoint; the broker independently validates
the active site and confines every path.

## Web wiring

`internal/web/handlers_files.go` exports this feature-local route registrar:

```go
func (s *Server) registerFileManagerRoutes(mux *http.ServeMux)
```

`Server.Handler` must call `s.registerFileManagerRoutes(mux)` once. Do not also
register the previous `GET /sites/{id}/files` and `POST /sites/{id}/files`
patterns because `http.ServeMux` rejects duplicate patterns. No new `pageData`
fields are required. The existing `Files`, `FilePath`, `DirectoryPath`,
`ParentPath`, `EditingFile`, `FileContent`, and `CanManageFiles` fields supply
the initial server-rendered page and editor.

The registrar owns these endpoints:

- `GET/POST /sites/{id}/files` render the directory/editor and save text.
- `GET .../api/list` returns a directory and `GET .../api/search` returns at
  most 200 matches beneath the submitted path.
- `GET .../api/download` streams validated 512 KiB broker chunks as an
  attachment without buffering the complete file.
- `POST .../api/upload` accepts one raw chunk capped at 512 KiB.
- `POST .../api/mkdir|delete|rename|archive|extract|copy|move` accept JSON capped
  at 64 KiB.

Every mutation requires the CSRF cookie and header/form token. All endpoints
require `site.files` for the requested site. The legacy editor form is capped
before parsing at 1 MiB plus form overhead.

## Upload recovery

The browser stores only the upload ID, destination, sampled SHA-256 fingerprint,
size, overwrite choice, and last server-confirmed offset. It never retains the
file contents and hashes at most 128 KiB of each local file. After reload, the
operator reselects the matching local file; upload continues from that offset.
The broker binds a journal to site, path, and upload ID. If a response is lost,
replaying the byte-identical chunk at an older offset returns the current
confirmed offset without rewriting it. Different replay bytes fail. Replaying
a completed final chunk returns the durable completed result.

## Synchronous mutations

Archive, extraction, copy, and move run synchronously through the web broker
client, whose production timeout is 15 seconds. The UI says `Working…` and
disables actions until a confirmed broker result arrives. It never presents a
queued or background state. Backend traversal limits cap recursive work at
10,000 entries, 256 MiB, depth 64, and 12 seconds. Uploads are capped at 1 GiB
and retain a 64 MiB free-space reserve. Work beyond those limits returns guidance
to narrow the selection instead of early success. When the response is
unknown, the UI retains context and tells the operator to refresh before retrying
because delete or move may already have changed some items.
