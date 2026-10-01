package vcard_test

import (
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
	for _, key := range []string{"fn", "uid", "n", "emails", "org", "tels", "tel_norm"} {
		if _, ok := meta[key]; !ok {
			t.Fatalf("search_meta missing key %q", key)
		}
	}
	if len(meta) != 7 {
		t.Fatalf("search_meta has %d keys, want 7: %v", len(meta), meta)
	}
	if meta["uid"] != uid {
		t.Fatalf("uid = %v, want %v", meta["uid"], uid)
	}
	norms := meta["tel_norm"].([]string)
	if len(norms) != 1 || norms[0] != "+442079460958" {
		t.Fatalf("tel_norm = %v", norms)
	}
}
