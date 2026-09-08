package webdavsvc_test

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/carddav"
)

// A client discovers the principal, home set, and address book, then asks the
// address book for getctag and the supported report set. This is the PROPFIND
// chain iOS/Thunderbird run, and it is what exercises the ctag shim.
func TestDeviceDiscovery(t *testing.T) {
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
	home, err := client.FindAddressBookHomeSet(ctx, principal)
	if err != nil {
		t.Fatalf("find home set: %v", err)
	}
	books, err := client.FindAddressBooks(ctx, home)
	if err != nil {
		t.Fatalf("find books: %v", err)
	}
	if len(books) != 1 {
		t.Fatalf("got %d address books, want 1", len(books))
	}
	ab := books[0].Path

	// PROPFIND depth 0 on the address book for getctag and the report set.
	body := `<?xml version="1.0"?>
<d:propfind xmlns:d="DAV:" xmlns:cs="http://calendarserver.org/ns/">
 <d:prop>
  <d:displayname/>
  <d:sync-token/>
  <cs:getctag/>
  <d:supported-report-set/>
 </d:prop>
</d:propfind>`
	req, err := http.NewRequest("PROPFIND", ts.URL+ab, bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("propfind req: %v", err)
	}
	req.Header.Set("Depth", "0")
	req.Header.Set("Content-Type", "application/xml")
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatalf("propfind: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("address book propfind = %d, want 207", resp.StatusCode)
	}
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	out := buf.String()
	for _, want := range []string{"displayname", "getctag", "sync-token", "sync-collection"} {
		if !strings.Contains(out, want) {
			t.Fatalf("address book propfind missing %q in:\n%s", want, out)
		}
	}
}
