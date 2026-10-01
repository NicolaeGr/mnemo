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
  - `POST /contacts` and `PATCH /contacts/{id}` take name parts, property rows, addresses, and tags; a raw `vcard` string also works.
  - `POST /events/force-resync`.
- **Admin UI** at `/login` then `/dashboard`: books, devices, contacts, and settings, server-rendered with htmx.

Requests authenticate either with a device token
(`Authorization: Bearer <token>`) or with Basic auth (the account password,
which sees every book). A token's tier gates which books it can see; password
auth is tier-all.

## Layout

```
cmd/server/            entrypoint: config, migrate, open pool, start workers
internal/
  auth/                bearer + basic resolution, actor in request context
  contact/             structured contact model (name, rows, addresses) shared
                       by the admin form and REST
  model/               plain structs + error sentinels
  store/               migrations (embed.FS) and scoped repositories
  resolve/             which books a caller can see (tier + overrides)
  api/                 /api/v1 handlers
  jobs/                outbox dispatcher + tombstone purge loop
  web/webdavsvc/       CardDAV over go-webdav, plus the sync/ctag shim
  web/admin/           dashboard handlers, session guard, CSRF
  web/pages/           templ pages and shared components
  web/layouts/         root, auth, dashboard, and modal shells
  websession/          HMAC session cookie + CSRF
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
with epoch bumps, device tokens, the REST surface, sync-collection REPORT, the
getctag address-book property, and the admin UI. CardDAV sync is verified
against go-webdav's client and by hand with vdirsyncer. Confirmation against
real iOS and Thunderbird clients is still pending.

Contact changes reach other clients within one dispatcher poll (250ms).

### Deviations from the contract

STRATEGY.MD is the design contract and is not committed to the repo. Where the
code differs, it is listed here rather than left implicit.

- **DAV paths.** Cards live at `/carddav/{username}/contacts/all/{filename}` and
  `.well-known/carddav` redirects to `/carddav/`, not `/dav/`. go-webdav derives
  every resource type from path depth, which rules out the contract's
  `/dav/contacts/{username}/` and `/dav/principals/{username}/` split. Clients
  find the context path through `.well-known/carddav`.
- **`search_meta` keys.** `fn, uid, n, emails, org, tels, tel_norm`. The contract
  freezes the same set without `n`, which was added for name search.
- **Phone normalization.** Digits plus a leading `+`, with a leading `00`
  rewritten to `+`. The contract's region-code prefixing needs a settings
  column and is not implemented.
- **PUT statuses.** A malformed vCard returns 400, not 415, and an update
  returns 201, not 204. Both come from the go-webdav handler.
- **REST surface.** Signup is `POST /api/v1/users`. There is no
  `/api/v1/session/login`; the UI logs in at `/login`.
- **Module names.** `internal/store` (not `storage`) and `internal/web/webdavsvc`
  (not `internal/dav`); the dispatcher sits in `internal/jobs`.

Not yet done: sharing and groups (a later phase).
