package webdavsvc_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/carddav"
)

// Wire-level checks of statuses the carddav client hides from us: a stale
// If-Match returns 412, an update onto a live UID returns 409, and re-creating
// a tombstoned filename returns 409.
func TestWireStatusCodes(t *testing.T) {
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
		t.Fatalf("find address books: %v", err)
	}
	base := ts.URL + books[0].Path

	// Fresh create is a 201 carrying an ETag.
	code, etag := putVCard(t, hc, base+"carol.vcf", "", vcardText("carol", "Carol"))
	if code != http.StatusCreated || etag == "" {
		t.Fatalf("create = %d (etag %q), want 201 with etag", code, etag)
	}

	// A stale If-Match on an existing resource returns 412 (the deliverable rule).
	if code, _ := putVCard(t, hc, base+"carol.vcf", `"not-the-current-etag"`, vcardText("carol", "Carol II")); code != http.StatusPreconditionFailed {
		t.Fatalf("stale If-Match PUT = %d, want 412", code)
	}

	// Re-creating a deleted filename returns 409 (the filename is retired).
	if code, _ := putVCard(t, hc, base+"dave.vcf", "", vcardText("dave", "Dave")); code != http.StatusCreated {
		t.Fatalf("create dave = %d, want 201", code)
	}
	req, err := http.NewRequest(http.MethodDelete, base+"dave.vcf", nil)
	if err != nil {
		t.Fatalf("delete req: %v", err)
	}
	if resp, err := hc.Do(req); err != nil {
		t.Fatalf("delete: %v", err)
	} else {
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("delete dave = %d, want 204", resp.StatusCode)
		}
	}
	if code, _ := putVCard(t, hc, base+"dave.vcf", "", vcardText("dave", "Dave II")); code != http.StatusConflict {
		t.Fatalf("re-create tombstoned dave = %d, want 409", code)
	}

	// Reusing frank's live UID when updating erin returns 409 (not a 500).
	codeE, etagE := putVCard(t, hc, base+"erin.vcf", "", vcardText("erin", "Erin"))
	if code, _ := putVCard(t, hc, base+"frank.vcf", "", vcardText("frank", "Frank")); code != http.StatusCreated {
		t.Fatalf("create frank = %d, want 201", code)
	}
	if codeE != http.StatusCreated || etagE == "" {
		t.Fatalf("create erin = %d (etag %q), want 201", codeE, etagE)
	}
	if code, _ := putVCard(t, hc, base+"erin.vcf", etagE, vcardText("frank", "Erin steals Frank's UID")); code != http.StatusConflict {
		t.Fatalf("erin update onto frank UID = %d, want 409", code)
	}
}

// putVCard PUTs a vcard body (with optional If-Match) and returns status + ETag.
func putVCard(t *testing.T, hc webdav.HTTPClient, url, ifMatch, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("put req: %v", err)
	}
	req.Header.Set("Content-Type", "text/vcard")
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode, resp.Header.Get("ETag")
}

func vcardText(uid, fn string) string {
	return "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:" + uid + "\r\nFN:" + fn + "\r\nEND:VCARD\r\n"
}
