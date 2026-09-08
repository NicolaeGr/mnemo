package webdavsvc_test

import (
	"context"
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

	// Unknown token: server returns a fresh token and no changes.
	first, err := client.SyncCollection(ctx, book, &carddav.SyncQuery{})
	if err != nil {
		t.Fatalf("sync (empty token): %v", err)
	}
	if first.SyncToken == "" || len(first.Updated) != 0 || len(first.Deleted) != 0 {
		t.Fatalf("empty-token sync = updated %d deleted %d token %q, want 0/0/fresh",
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
