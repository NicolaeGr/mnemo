# mnemo

A single-binary CardDAV contact server backed by Postgres. Each user has one
virtual address book whose contents resolve per authenticating credential at
request time, so contacts can be organised into tags (books) and different
devices can be shown different slices without any of them knowing.

## Running

The dev environment is devenv (Postgres + Redis).

```sh
devenv shell
go run ./cmd/server
```

On boot the server migrates the schema and, if empty, seeds an `admin` user
(`admin@example.com` / `password`). It listens on `:8080`.

## What it exposes

- **CardDAV** at `/carddav/`:
  - GET/PUT/DELETE of `text/vcard` address objects.
  - REPORT `addressbook-query` / `addressbook-multiget`.
  - REPORT `sync-collection` (RFC 6578) for incremental sync.
  - address-book PROPFIND with `getctag` / `supported-report-set`.
  - `.well-known/carddav` -> 301 `/carddav/`.
- **REST** at `/api/v1` for managing the parts CardDAV can't:
  - `POST /principals` mints a device token (returned once).
  - `GET|POST /books`, `PATCH|DELETE /books/{id}`.
  - `POST /contacts/{id}/tags` to attach/detach books.
  - `GET /contacts?q=` to search by name or phone digits.
  - `POST /events/force-resync`.

Requests authenticate either with a device token
(`Authorization: Bearer <token>`) or with Basic auth (the account password,
which sees every book). A token's tier gates which books it can see; password
auth is tier-all.

## Layout

```
cmd/server/            entrypoint: config, migrate, open pool, start workers
internal/
  auth/                bearer + basic resolution, actor in request context
  model/               plain structs + error sentinels
  store/               migrations (embed.FS) and scoped repositories
  resolve/             which books a caller can see (tier + overrides)
  api/                 /api/v1 handlers
  jobs/                outbox dispatcher + tombstone purge loop
  web/webdavsvc/       CardDAV over go-webdav, plus the sync/ctag shim
  testdb/              per-package isolated Postgres databases for tests
```

One CardDAV request that mutates data is one transaction. Writes that touch the
sync stream or a principal's epoch take a per-user advisory lock so change
ordering stays consistent. Repository reads are woven with the acting user so
no query crosses users.

## Testing

Tests that touch Postgres each use their own database and reset it before
running, so `go test ./...` runs packages in parallel safely.

```sh
go test ./...
```

## Status

Working: schema + migrations, signup, contact CRUD with etags and a per-user
change stream, soft delete with purge, tag attach/detach and book operations
with epoch bumps, device tokens, minimal REST, sync-collection REPORT, and the
getctag address-book property. CardDAV sync is verified against go-webdav's
client; final confirmation against real iOS/Thunderbird clients is still
pending, as is the exact `getctag` namespace each expects.

Not yet done: the web UI, full REST session/CSRF flow, and sharing (a later
phase).
