package webdavsvc_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/emersion/go-webdav"
)

// A depth-1 PROPFIND that asks only for metadata is answered from the light
// {filename, etag} listing, while one that asks for address-data must still
// carry the card bodies. Both must succeed (a missing Card would be a 500).
func TestPropfindCardDataModes(t *testing.T) {
	ts, pool := newTestServer(t)
	defer ts.Close()
	defer pool.Close()

	hc := webdav.HTTPClientWithBasicAuth(ts.Client(), testUsername, testPassword)
	putURL := ts.URL + "/carddav/" + testUsername + "/contacts/all/pf.vcf"
	body := "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:urn:uuid:pf\r\nFN:Prop Find\r\nEND:VCARD\r\n"
	if code, _ := putVCard(t, hc, putURL, "", body); code != http.StatusCreated {
		t.Fatalf("seed put = %d, want 201", code)
	}

	ab := ts.URL + "/carddav/" + testUsername + "/contacts/all/"

	etagOnly := `<?xml version="1.0" encoding="utf-8"?>` +
		`<d:propfind xmlns:d="DAV:"><d:prop><d:getetag/><d:resourcetype/></d:prop></d:propfind>`
	if code, respBody := propfind(t, hc, ab, etagOnly); code != http.StatusMultiStatus {
		t.Fatalf("etag-only PROPFIND = %d, body:\n%s", code, respBody)
	} else if !strings.Contains(respBody, "pf.vcf") {
		t.Fatalf("etag-only PROPFIND missing the card:\n%s", respBody)
	}

	withData := `<?xml version="1.0" encoding="utf-8"?>` +
		`<d:propfind xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:carddav">` +
		`<d:prop><d:getetag/><c:address-data/></d:prop></d:propfind>`
	if code, respBody := propfind(t, hc, ab, withData); code != http.StatusMultiStatus {
		t.Fatalf("address-data PROPFIND = %d, body:\n%s", code, respBody)
	} else if !strings.Contains(respBody, "FN:Prop Find") {
		t.Fatalf("address-data PROPFIND missing the card body:\n%s", respBody)
	}
}

func propfind(t *testing.T, hc webdav.HTTPClient, url, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest("PROPFIND", url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("propfind req: %v", err)
	}
	req.Header.Set("Depth", "1")
	req.Header.Set("Content-Type", "application/xml")
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatalf("propfind: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}
