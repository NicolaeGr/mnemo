package vcard_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/emersion/go-vcard"

	vcardmeta "github.com/nicolaegr/mnemo/internal/vcard"
)

func TestEnsureUID(t *testing.T) {
	card := vcard.Card{}
	if got := vcardmeta.EnsureUID(card, "abc123.vcf"); got != "abc123" {
		t.Fatalf("derived uid = %q, want abc123", got)
	}
	if got := card.Value(vcard.FieldUID); got != "abc123" {
		t.Fatalf("card UID = %q, want it written onto the card", got)
	}

	card.SetValue(vcard.FieldUID, "urn:uuid:kept")
	if got := vcardmeta.EnsureUID(card, "abc123.vcf"); got != "urn:uuid:kept" {
		t.Fatalf("existing uid = %q, want it left alone", got)
	}
}

func TestFromRawPreservesClientBytes(t *testing.T) {
	raw := "BEGIN:VCARD\r\n" +
		"VERSION:3.0\r\n" +
		"UID:urn:uuid:abc\r\n" +
		"FN:Jane Doe\r\n" +
		"X-CUSTOM;FOO=bar:keep me\r\n" +
		"EMAIL;TYPE=work:jane@example.com\r\n" +
		"END:VCARD\r\n"

	text, uid, meta, err := vcardmeta.FromRaw(raw, "abc.vcf")
	if err != nil {
		t.Fatalf("FromRaw: %v", err)
	}
	if uid != "urn:uuid:abc" {
		t.Fatalf("uid = %q, want the client's UID", uid)
	}
	if strings.Contains(text, "\r") {
		t.Fatalf("CRs not stripped:\n%q", text)
	}
	// Unknown properties and parameter order must survive verbatim.
	for _, want := range []string{"X-CUSTOM;FOO=bar:keep me", "EMAIL;TYPE=work:jane@example.com"} {
		if !strings.Contains(text, want) {
			t.Fatalf("stored body lost %q:\n%s", want, text)
		}
	}

	var m map[string]any
	if err := json.Unmarshal(meta, &m); err != nil {
		t.Fatalf("meta json: %v", err)
	}
	if m["fn"] != "Jane Doe" {
		t.Fatalf("search fn = %v, want Jane Doe", m["fn"])
	}
}

func TestFromRawSplicesMissingFNAndUID(t *testing.T) {
	text, uid, _, err := vcardmeta.FromRaw("BEGIN:VCARD\r\nVERSION:3.0\r\nEND:VCARD\r\n", "9f2a.vcf")
	if err != nil {
		t.Fatalf("FromRaw: %v", err)
	}
	if uid != "9f2a" {
		t.Fatalf("derived uid = %q, want 9f2a", uid)
	}
	if !strings.Contains(text, "UID:9f2a") {
		t.Fatalf("missing UID splice:\n%s", text)
	}
	if !strings.Contains(text, "FN:") {
		t.Fatalf("missing FN splice:\n%s", text)
	}
	if strings.Index(text, "UID:9f2a") > strings.Index(text, "END:VCARD") {
		t.Fatalf("spliced property landed after END:VCARD:\n%s", text)
	}
}

func TestFromRawReFoldsOverlongLines(t *testing.T) {
	long := strings.Repeat("a", 200)
	raw := "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:u\r\nFN:" + long + "\r\nEND:VCARD\r\n"
	text, _, _, err := vcardmeta.FromRaw(raw, "u.vcf")
	if err != nil {
		t.Fatalf("FromRaw: %v", err)
	}
	for _, l := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		if len(l) > 75 {
			t.Fatalf("line of %d octets not folded: %q", len(l), l)
		}
	}
	// Re-parsing the folded body must recover the original value.
	card, err := vcard.NewDecoder(strings.NewReader(text)).Decode()
	if err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if got := card.Value(vcard.FieldFormattedName); got != long {
		t.Fatalf("FN round-trip = %d chars, want %d", len(got), len(long))
	}
}
func TestNormalizePhone(t *testing.T) {
	cases := map[string]string{
		"+1 (555) 0100":  "+15550100",
		"0049 30 123456": "+4930123456",
		"(555) 0100":     "5550100",
		"+49+30":         "+4930",
		"00 44 20 7946":  "+44207946",
		"no digits here": "",
		"tel:+15550100":  "+15550100",
	}
	for in, want := range cases {
		if got := vcardmeta.NormalizePhone(in); got != want {
			t.Errorf("NormalizePhone(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSearchMetaShape(t *testing.T) {
	card := vcard.Card{}
	card.SetValue(vcard.FieldVersion, "3.0")
	uid := vcardmeta.EnsureUID(card, "card.vcf")
	card.SetValue(vcard.FieldFormattedName, "Jane Doe")
	card.SetValue(vcard.FieldTelephone, "00 44 20 7946 0958")

	meta := vcardmeta.SearchMeta(card)
	for _, key := range []string{"fn", "uid", "n", "sort", "emails", "org", "tels", "tel_norm"} {
		if _, ok := meta[key]; !ok {
			t.Fatalf("search_meta missing key %q", key)
		}
	}
	if len(meta) != 8 {
		t.Fatalf("search_meta has %d keys, want 8: %v", len(meta), meta)
	}
	if meta["sort"] != "Jane Doe" {
		t.Fatalf("sort = %v, want Jane Doe", meta["sort"])
	}
	if meta["uid"] != uid {
		t.Fatalf("uid = %v, want %v", meta["uid"], uid)
	}
	norms := meta["tel_norm"].([]string)
	if len(norms) != 1 || norms[0] != "+442079460958" {
		t.Fatalf("tel_norm = %v", norms)
	}
}
