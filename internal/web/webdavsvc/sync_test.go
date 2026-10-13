package webdavsvc_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/carddav"
)

func TestSyncCollection(t *testing.T) {
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

	books, err := client.FindAddressBooks(ctx, "/carddav/"+testUsername+"/contacts/")
	if err != nil {
		t.Fatalf("find books: %v", err)
	}
	book := books[0].Path

	put := func(fn, uid string) {
		t.Helper()
		card := vcard.Card{}
		card.SetValue(vcard.FieldVersion, "3.0")
		card.SetValue(vcard.FieldUID, uid)
		card.SetValue(vcard.FieldFormattedName, fn)
		if _, err := client.PutAddressObject(ctx, book+fn+".vcf", card); err != nil {
			t.Fatalf("put %s: %v", fn, err)
		}
	}
	put("alice", "urn:uuid:sync-a")
	put("bob", "urn:uuid:sync-b")

	// Initial sync (empty token, RFC 6578): the server reports every current
	// member, so a client that trusts this report still sees pre-existing cards.
	first, err := client.SyncCollection(ctx, book, &carddav.SyncQuery{})
	if err != nil {
		t.Fatalf("sync (empty token): %v", err)
	}
	if first.SyncToken == "" || len(first.Updated) != 2 || len(first.Deleted) != 0 {
		t.Fatalf("initial sync = updated %d deleted %d token %q, want 2/0/fresh",
			len(first.Updated), len(first.Deleted), first.SyncToken)
	}

	// Mutate one card and delete the other, then ask for the delta.
	put("bob", "urn:uuid:sync-b") // content change -> a new put change
	if err := client.RemoveAll(ctx, book+"alice.vcf"); err != nil {
		t.Fatalf("delete alice: %v", err)
	}

	delta, err := client.SyncCollection(ctx, book, &carddav.SyncQuery{SyncToken: first.SyncToken})
	if err != nil {
		t.Fatalf("sync delta: %v", err)
	}
	if len(delta.Deleted) != 1 || !strings.HasSuffix(delta.Deleted[0], "alice.vcf") {
		t.Fatalf("delta deleted = %v, want [alice.vcf]", delta.Deleted)
	}
	if len(delta.Updated) != 1 || !strings.HasSuffix(delta.Updated[0].Path, "bob.vcf") {
		t.Fatalf("delta updated = %+v, want [bob.vcf]", delta.Updated)
	}
	if delta.SyncToken == first.SyncToken {
		t.Fatalf("delta token did not advance: %q", delta.SyncToken)
	}
}

// A token the server cannot honor, including one left stale by a tier flip, must
// answer 403 DAV:valid-sync-token so the client re-lists. Reporting an empty
// collection instead makes a client discard its local cards.
func TestSyncCollectionRejectsStaleToken(t *testing.T) {
	ts, pool := newTestServer(t)
	defer ts.Close()
	defer pool.Close()

	book := "/carddav/" + testUsername + "/contacts/all/"
	body := `<?xml version="1.0"?><d:sync-collection xmlns:d="DAV:">` +
		`<d:sync-token>bogus-9-9</d:sync-token><d:sync-level>1</d:sync-level>` +
		`<d:prop><d:getetag/></d:prop></d:sync-collection>`
	req, err := http.NewRequest("REPORT", ts.URL+book, strings.NewReader(body))
	if err != nil {
		t.Fatalf("req: %v", err)
	}
	req.Header.Set("Content-Type", "application/xml")
	req.SetBasicAuth(testUsername, testPassword)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("stale token = %d, want 403. body: %s", resp.StatusCode, out)
	}
	if !strings.Contains(string(out), "valid-sync-token") {
		t.Fatalf("403 body missing valid-sync-token: %s", out)
	}
}
