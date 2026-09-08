package webdavsvc_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/carddav"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"example.com/segments/internal/store"
	"example.com/segments/internal/web/webdavsvc"
)

const (
	testUsername = "nicolae"
	testEmail    = "nicolae@electrolit.biz"
	testPassword = "password123"
)

func TestCardDAVRoundTrip(t *testing.T) {
	ts, pool := newTestServer(t)
	defer ts.Close()
	defer pool.Close()

	hc := webdav.HTTPClientWithBasicAuth(ts.Client(), testUsername, testPassword)
	client, err := carddav.NewClient(hc, ts.URL)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	principal, err := client.FindCurrentUserPrincipal(ctx)
	if err != nil {
		t.Fatalf("find principal: %v", err)
	}
	if want := "/carddav/" + testUsername + "/"; principal != want {
		t.Fatalf("principal = %q, want %q", principal, want)
	}

	homeSet, err := client.FindAddressBookHomeSet(ctx, principal)
	if err != nil {
		t.Fatalf("find home set: %v", err)
	}
	if want := "/carddav/" + testUsername + "/contacts/"; homeSet != want {
		t.Fatalf("home set = %q, want %q", homeSet, want)
	}
	books, err := client.FindAddressBooks(ctx, homeSet)
	if err != nil {
		t.Fatalf("find address books: %v", err)
	}
	if len(books) != 1 {
		t.Fatalf("got %d address books, want 1", len(books))
	}
	book := books[0].Path

	card := vcard.Card{}
	card.SetValue(vcard.FieldUID, "urn:uuid:0000-0000-0000-0001")
	card.SetValue(vcard.FieldVersion, "3.0")
	card.SetValue(vcard.FieldFormattedName, "Alice Example")
	card.SetValue(vcard.FieldTelephone, "+1-555-0100")
	card.SetValue(vcard.FieldEmail, "alice@example.com")
	objPath := book + "alice.vcf"

	created, err := client.PutAddressObject(ctx, objPath, card)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if created.ETag == "" {
		t.Fatal("created object has no ETag")
	}
	assertETagIsBodyHash(t, pool, testUsername, created.ETag)

	listed, err := client.QueryAddressBook(ctx, book, &carddav.AddressBookQuery{})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(listed) != 1 || listed[0].Path != objPath {
		t.Fatalf("list = %+v, want exactly [%s]", listed, objPath)
	}

	got, err := client.GetAddressObject(ctx, objPath)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if fn := got.Card.Value(vcard.FieldFormattedName); fn != "Alice Example" {
		t.Fatalf("FN = %q, want Alice Example", fn)
	}

	card.SetValue(vcard.FieldFormattedName, "Alice Example II")
	updated, err := client.PutAddressObject(ctx, objPath, card)
	if err != nil {
		t.Fatalf("put update: %v", err)
	}
	if updated.ETag == "" || updated.ETag == created.ETag {
		t.Fatalf("update ETag = %q (created %q), want a change", updated.ETag, created.ETag)
	}

	if err := client.RemoveAll(ctx, objPath); err != nil {
		t.Fatalf("delete: %v", err)
	}
	after, err := client.QueryAddressBook(ctx, book, &carddav.AddressBookQuery{})
	if err != nil {
		t.Fatalf("query after delete: %v", err)
	}
	if len(after) != 0 {
		t.Fatalf("after delete got %d objects, want 0", len(after))
	}
}

func assertETagIsBodyHash(t *testing.T, pool *pgxpool.Pool, username string, etag string) {
	t.Helper()
	var vcardText string
	if err := pool.QueryRow(context.Background(),
		`SELECT vcard_text FROM contacts c JOIN users u ON u.id = c.user_id WHERE u.username = $1`,
		username).Scan(&vcardText); err != nil {
		t.Fatalf("fetch stored vcard: %v", err)
	}
	sum := sha256Hex(vcardText)
	if got := strings.Trim(etag, `"`); got != sum {
		t.Fatalf("ETag %q != sha256(vcard) %q (invariant I3 violated)", etag, sum)
	}
}

// TestChangeStreamAndSoftDelete pins the §5.3/§5.4 storage contract behind the
// DAV layer: every PUT appends exactly one 'put' change, DELETE soft-deletes
// (row stays, deleted_at set) and appends one 'delete' change.
func TestChangeStreamAndSoftDelete(t *testing.T) {
	ts, pool := newTestServer(t)
	defer ts.Close()
	defer pool.Close()

	hc := webdav.HTTPClientWithBasicAuth(ts.Client(), testUsername, testPassword)
	client, err := carddav.NewClient(hc, ts.URL)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	ctx := context.Background()

	books, err := client.FindAddressBooks(ctx, "/carddav/"+testUsername+"/contacts/")
	if err != nil {
		t.Fatalf("find books: %v", err)
	}
	objPath := books[0].Path + "bob.vcf"

	card := vcard.Card{}
	card.SetValue(vcard.FieldVersion, "3.0")
	card.SetValue(vcard.FieldUID, "urn:uuid:0000-0000-0000-0002")
	card.SetValue(vcard.FieldFormattedName, "Bob Example")

	if _, err := client.PutAddressObject(ctx, objPath, card); err != nil {
		t.Fatalf("put create: %v", err)
	}
	card.SetValue(vcard.FieldFormattedName, "Bob Example II")
	if _, err := client.PutAddressObject(ctx, objPath, card); err != nil {
		t.Fatalf("put update: %v", err)
	}

	changes := countChanges(t, pool, testUsername)
	if len(changes) != 2 || changes[0].Type != "put" || changes[1].Type != "put" {
		t.Fatalf("changes after create+update = %+v, want two puts", changes)
	}

	if err := client.RemoveAll(ctx, objPath); err != nil {
		t.Fatalf("delete: %v", err)
	}
	changes = countChanges(t, pool, testUsername)
	if len(changes) != 3 || changes[2].Type != "delete" {
		t.Fatalf("changes after delete = %+v, want third row of type delete", changes)
	}

	// Soft delete: the row survives with deleted_at set (I4 / §5.4).
	var deletedAt *time.Time
	var filename string
	if err := pool.QueryRow(ctx,
		`SELECT filename, deleted_at FROM contacts c
		  JOIN users u ON u.id = c.user_id
		 WHERE u.username = $1 AND c.filename = 'bob.vcf'`,
		testUsername).Scan(&filename, &deletedAt); err != nil {
		t.Fatalf("fetch tombstone: %v", err)
	}
	if deletedAt == nil {
		t.Fatal("DELETE hard-deleted the row; §5.4 requires a soft delete")
	}
	if filename != "bob.vcf" {
		t.Fatalf("tombstoned filename = %q, want bob.vcf", filename)
	}

	// Live-UID uniqueness index still sees the tombstone-holder as gone, so a
	// fresh card under a NEW filename with the same UID must succeed.
	card2 := vcard.Card{}
	card2.SetValue(vcard.FieldVersion, "3.0")
	card2.SetValue(vcard.FieldUID, "urn:uuid:0000-0000-0000-0002")
	card2.SetValue(vcard.FieldFormattedName, "Bob Reborn")
	if _, err := client.PutAddressObject(ctx, books[0].Path+"bob2.vcf", card2); err != nil {
		t.Fatalf("recreate same UID under new filename after delete: %v", err)
	}
}

type changeRow struct {
	Type string
}

func countChanges(t *testing.T, pool *pgxpool.Pool, username string) []changeRow {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT cc.change_type FROM contact_changes cc
		JOIN users u ON u.id = cc.user_id
		WHERE u.username = $1 ORDER BY cc.id`, username)
	if err != nil {
		t.Fatalf("query changes: %v", err)
	}
	defer rows.Close()
	out := make([]changeRow, 0)
	for rows.Next() {
		var c changeRow
		if err := rows.Scan(&c.Type); err != nil {
			t.Fatalf("scan change: %v", err)
		}
		out = append(out, c)
	}
	return out
}

func newTestServer(t *testing.T) (*httptest.Server, *pgxpool.Pool) {
	t.Helper()

	pool := testPool(t)

	users := store.NewUsers(pool)
	if _, _, err := users.Signup(context.Background(), testUsername, testEmail, "Nick", testPassword, bcrypt.DefaultCost); err != nil {
		t.Fatalf("signup: %v", err)
	}

	handler := webdavsvc.New(users, pool)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	t.Cleanup(pool.Close)
	return ts, pool
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), testDatabaseURL())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		t.Fatalf("ping: %v", err)
	}
	// Fresh schema every run so migration ordering/versions never leak state.
	if _, err := pool.Exec(context.Background(), `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		pool.Close()
		t.Fatalf("drop schema: %v", err)
	}
	if err := store.Migrate(context.Background(), pool); err != nil {
		pool.Close()
		t.Fatalf("migrate: %v", err)
	}
	return pool
}

// testDatabaseURL mirrors config.Load's DSN construction so tests run against
// the same devenv-provided Postgres as the app.
func testDatabaseURL() string {
	if v := os.Getenv("DATABASE_URL"); v != "" {
		return v
	}
	host := envOr("PGHOST", "/run/user/1000/devenv-386ec41/postgres")
	port := envOr("PGPORT", "5432")
	user := envOr("PGUSER", os.Getenv("USER"))
	db := envOr("PGDATABASE", os.Getenv("USER"))
	return "host=" + host + " port=" + port + " user=" + user + " dbname=" + db + " sslmode=disable"
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
