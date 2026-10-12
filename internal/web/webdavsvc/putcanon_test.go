package webdavsvc_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/emersion/go-webdav"
)

// A CardDAV PUT stores a byte-level normalization of what the client sent:
// unknown properties and parameter order survive, CRs are stripped, and the
// stored etag is the hash of that exact body (I3).
func TestPutPreservesClientBytes(t *testing.T) {
	ts, pool := newTestServer(t)
	defer ts.Close()
	defer pool.Close()

	hc := webdav.HTTPClientWithBasicAuth(ts.Client(), testUsername, testPassword)
	body := "BEGIN:VCARD\r\n" +
		"VERSION:3.0\r\n" +
		"UID:urn:uuid:canon-1\r\n" +
		"FN:Canon Tester\r\n" +
		"X-MNEMO-OPAQUE;P=1:kept\r\n" +
		"TEL;TYPE=cell;PREF=1:+1 555 0100\r\n" +
		"END:VCARD\r\n"

	url := ts.URL + "/carddav/" + testUsername + "/contacts/all/canon.vcf"
	req, err := http.NewRequest(http.MethodPut, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("put req: %v", err)
	}
	req.Header.Set("Content-Type", "text/vcard")
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT = %d, want 201", resp.StatusCode)
	}

	var stored string
	if err := pool.QueryRow(context.Background(),
		`SELECT vcard_text FROM contacts c JOIN users u ON u.id = c.user_id WHERE u.username = $1`,
		testUsername).Scan(&stored); err != nil {
		t.Fatalf("read stored vcard: %v", err)
	}
	if strings.Contains(stored, "\r") {
		t.Fatalf("stored body has CRs:\n%q", stored)
	}
	for _, want := range []string{"X-MNEMO-OPAQUE;P=1:kept", "TEL;TYPE=cell;PREF=1:+1 555 0100"} {
		if !strings.Contains(stored, want) {
			t.Fatalf("stored body lost %q:\n%s", want, stored)
		}
	}
	assertETagIsBodyHash(t, pool, testUsername, resp.Header.Get("ETag"))
}
