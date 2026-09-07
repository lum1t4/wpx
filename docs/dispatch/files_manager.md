# Confined file manager backend

The full file manager extends the existing editor without changing
`broker.FileOperator`. `broker.FileManagerOperator` is an optional interface on
`Server.Files`; Linux `*provision.Host` implements it.

## Broker integration

The central broker dispatcher must call the feature helper before its original
switch:

```go
if response, handled := s.dispatchFileManager(request); handled {
	return response
}
```

The helper owns strict decoding and validation for `file.search`,
`file.download`, `file.upload_chunk`, `file.delete`, `file.rename`,
`file.mkdir`, `file.archive`, `file.extract`, `file.copy`, and `file.move`.
Every request carries the complete authorized `model.Site`. The broker accepts
only a valid active site, normalized relative paths, at most 100 disjoint
selected paths, and operation-specific bounds. Empty and `.` mean the public
root only for operations that explicitly allow the root.

`broker.Client` exposes typed methods for each operation. Binary transport uses
Go `[]byte`, encoded as base64 by JSON. `broker.FileChunkBytes` is 512 KiB, which
keeps a request or response below the broker's 2 MiB envelope limit.

## Upload recovery

Uploads stage beside their destination and publish with a descriptor-relative
rename. An upload ID contains 16 to 128 ASCII letters, digits, underscores, or
hyphens. A private journal under
`DataRoot/file-uploads/<site-id>/` binds the ID to the site and normalized path,
records the confirmed byte offset, and records the publishing and complete
states. The staging name is also derived from the ID and path.

Each append uses `WriteAt` at the exact confirmed offset and fsyncs the file
before advancing the journal. If a response is lost, replaying the same chunk
at an older offset compares its bytes and returns the current `next_offset`
without rewriting. A replay with different bytes fails. The browser should
adopt a successful response's `next_offset`. Replaying the final request after
publication returns `complete: true`. Completed upload IDs cannot be reused for
a new upload; clients generate a new random ID.

Uploads are limited to 1 GiB per file and preserve a 64 MiB free-space reserve.
Existing directories and symlinks cannot be upload overwrite targets. New
files, copied files, archives, and extracted content are chowned to the site's
identity.

## Confinement and bulk bounds

All public-tree traversal starts from an `openat2` descriptor with
`RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS`. Descendant directories are opened the
same way, and mutations use `*at` system calls. Selected symlinks and special
files are rejected. Search does not follow symlinks. Rename does not overwrite.
Copy publishes a staged entry. Archive creation supports ZIP. Extraction
supports ZIP, TAR, TAR.GZ, and TGZ; absolute paths, `..`, non-canonical names,
symlinks, hard links, and special members are rejected.

Search scans at most 10,000 entries and returns at most 500 matches. Recursive
copy, overwrite preflight, delete preflight, archive, and extraction permit at
most 10,000 entries, 256 MiB, 64 levels, and 12 seconds. These operations run
synchronously within the current broker and HTTP timeouts; an exceeded bound
asks the operator to choose a narrower selection. Multi-selection operations
commit one top-level selection at a time. An error after an earlier selection
can leave those earlier changes in place. Extraction can create earlier archive
members before a later invalid/conflicting member is found; its error reports
the count and tells the UI to refresh before retrying.

