package vcard_test

import (
	"testing"

	"github.com/emersion/go-vcard"

	vcardmeta "github.com/nicolaegr/mnemo/internal/vcard"
)

func TestSortName(t *testing.T) {
	card := vcard.Card{}
	card.SetValue(vcard.FieldFormattedName, "Dr. Jane Doe")
	card.SetValue(vcard.FieldName, "Doe;Jane;Quinn;Dr.;Jr.")
	if got, want := vcardmeta.SortName(card), "Doe Jane Quinn Dr. Jr."; got != want {
		t.Fatalf("SortName = %q, want %q", got, want)
	}

	given := vcard.Card{}
	given.SetValue(vcard.FieldFormattedName, "Jane")
	given.SetValue(vcard.FieldName, ";Jane;;;")
	if got, want := vcardmeta.SortName(given), "Jane"; got != want {
		t.Fatalf("given-only SortName = %q, want %q", got, want)
	}

	fn := vcard.Card{}
	fn.SetValue(vcard.FieldFormattedName, "Fallback Name")
	if got, want := vcardmeta.SortName(fn), "Fallback Name"; got != want {
		t.Fatalf("FN SortName = %q, want %q", got, want)
	}

	empty := vcard.Card{}
	empty.SetValue(vcard.FieldName, ";;;")
	empty.SetValue(vcard.FieldFormattedName, "FN Wins")
	if got, want := vcardmeta.SortName(empty), "FN Wins"; got != want {
		t.Fatalf("empty-name SortName = %q, want %q", got, want)
	}
}

func TestPhotoURL(t *testing.T) {
	cases := []struct {
		name string
		card vcard.Card
		want string
	}{
		{
			name: "data URL passthrough",
			card: photoCard("data:image/png;base64,AAAA", nil),
			want: "data:image/png;base64,AAAA",
		},
		{
			name: "https passthrough",
			card: photoCard("https://example.com/photo.jpg", nil),
			want: "https://example.com/photo.jpg",
		},
		{
			name: "http passthrough",
			card: photoCard("http://example.com/photo.jpg", nil),
			want: "http://example.com/photo.jpg",
		},
		{
			name: "v3 base64 with type",
			card: photoCard("iVBORw0KGgo=", vcard.Params{
				"ENCODING": {"b"},
				"TYPE":     {"PNG"},
			}),
			want: "data:image/png;base64,iVBORw0KGgo=",
		},
		{
			name: "default jpeg strips whitespace",
			card: photoCard("AAAA BBBB", vcard.Params{"ENCODING": {"b"}}),
			want: "data:image/jpeg;base64,AAAABBBB",
		},
		{
			name: "v4 mediatype",
			card: photoCard("AAAA", vcard.Params{"MEDIATYPE": {"image/webp"}}),
			want: "data:image/webp;base64,AAAA",
		},
		{
			name: "blank first field",
			card: photoCard("   ", nil),
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := vcardmeta.PhotoURL(tc.card); got != tc.want {
				t.Fatalf("PhotoURL = %q, want %q", got, tc.want)
			}
		})
	}
}

func photoCard(value string, params vcard.Params) vcard.Card {
	return vcard.Card{
		vcard.FieldPhoto: []*vcard.Field{{Value: value, Params: params}},
	}
}
