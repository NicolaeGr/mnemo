package vcard_test

import (
	"slices"
	"strings"
	"testing"

	vcardmeta "github.com/nicolaegr/mnemo/internal/vcard"
)

func TestWithCategories(t *testing.T) {
	body := "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Jane Doe\r\nUID:urn:uuid:1\r\nEND:VCARD\r\n"

	got := vcardmeta.WithCategories(body, []string{"work", "friends"})
	want := "BEGIN:VCARD\nVERSION:3.0\nFN:Jane Doe\nUID:urn:uuid:1\nCATEGORIES:work\nCATEGORIES:friends\nEND:VCARD\n"
	if got != want {
		t.Fatalf("with categories =\n%q\nwant\n%q", got, want)
	}

	// Replacing an existing CATEGORIES keeps every other line and its order.
	withOld := "BEGIN:VCARD\nFN:Jane Doe\nCATEGORIES:old\nUID:urn:uuid:1\nEND:VCARD\n"
	got = vcardmeta.WithCategories(withOld, []string{"new"})
	if strings.Contains(got, "CATEGORIES:old") || !strings.Contains(got, "CATEGORIES:new\n") {
		t.Fatalf("replace = %q, want only CATEGORIES:new", got)
	}

	// Empty names remove the property.
	got = vcardmeta.WithCategories(withOld, nil)
	if strings.Contains(got, "CATEGORIES") {
		t.Fatalf("remove = %q, want no CATEGORIES", got)
	}

	// A name containing a comma is escaped so it stays one value.
	got = vcardmeta.WithCategories(body, []string{"a,b"})
	if !strings.Contains(got, "CATEGORIES:a\\,b\n") {
		t.Fatalf("escape = %q, want CATEGORIES:a\\,b", got)
	}
}

func TestCategories(t *testing.T) {
	// Repeated properties, one value each.
	raw, vals := vcardmeta.Categories("BEGIN:VCARD\nCATEGORIES:Work\nCATEGORIES:Personal\nEND:VCARD\n")
	if len(raw) != 2 || !slices.Equal(vals, []string{"Work", "Personal"}) {
		t.Fatalf("repeated: raw=%q vals=%q", raw, vals)
	}

	// A comma list plus an escaped comma inside one value.
	_, vals = vcardmeta.Categories("BEGIN:VCARD\nCATEGORIES:Work,Personal\nCATEGORIES:a\\,b\nEND:VCARD\n")
	if !slices.Equal(vals, []string{"Work", "Personal", "a,b"}) {
		t.Fatalf("mixed vals = %q", vals)
	}

	if raw, vals := vcardmeta.Categories("BEGIN:VCARD\nFN:X\nEND:VCARD\n"); len(raw) != 0 || len(vals) != 0 {
		t.Fatalf("none: raw=%q vals=%q", raw, vals)
	}
}
