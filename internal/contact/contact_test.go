package contact_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/emersion/go-vcard"

	"github.com/nicolaegr/mnemo/internal/contact"
	"github.com/nicolaegr/mnemo/internal/model"
)

func TestBuildParseRoundTrip(t *testing.T) {
	base := vcard.Card{}
	base.SetValue(vcard.FieldUID, "urn:uuid:keep")
	base.SetValue(vcard.FieldVersion, "3.0")
	base.SetValue(vcard.FieldPhoto, "data:image/png;base64,AAAA")

	form := contact.Form{
		Name: contact.Name{Prefix: "Dr.", Given: "Alice", Family: "Smith"},
		Fields: []contact.Field{
			{Kind: "tel", Type: "cell", Value: "+15550100"},
			{Kind: "email", Type: "work", Value: "alice@example.com"},
			{Kind: "custom", Key: "x-foo", Value: "bar"},
			{Kind: "org", Value: "Acme"},
		},
		Addresses: []contact.Address{{Type: "work", Street: "1 Main St", City: "Town", Country: "US"}},
	}

	card, err := contact.Build(base, form)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if got := card.Value(vcard.FieldFormattedName); got != "Dr. Alice Smith" {
		t.Fatalf("FN = %q", got)
	}
	if got := card.Value(vcard.FieldName); got != "Smith;Alice;;Dr.;" {
		t.Fatalf("N = %q", got)
	}
	if got := card.Value(vcard.FieldUID); got != "urn:uuid:keep" {
		t.Fatalf("UID not preserved: %q", got)
	}
	if got := card.Value(vcard.FieldPhoto); got == "" {
		t.Fatal("PHOTO dropped")
	}
	// A lowercase custom key is uppercased.
	if got := card.Value("X-FOO"); got != "bar" {
		t.Fatalf("X-FOO = %q", got)
	}

	round := contact.Parse(card)
	if round.Name != form.Name {
		t.Fatalf("name round-trip = %+v, want %+v", round.Name, form.Name)
	}
	if len(round.Addresses) != 1 || round.Addresses[0].Street != "1 Main St" || round.Addresses[0].Type != "work" {
		t.Fatalf("address round-trip = %+v", round.Addresses)
	}

	var kinds []string
	for _, f := range round.Fields {
		kinds = append(kinds, f.Kind)
	}
	got := strings.Join(kinds, ",")
	if got != "email,org,tel,custom" {
		t.Fatalf("field kinds = %q", got)
	}
	for _, f := range round.Fields {
		if f.Kind == "custom" && f.Key != "X-FOO" {
			t.Fatalf("custom key = %q", f.Key)
		}
	}
}

func TestBuildRejectsBadCustomKey(t *testing.T) {
	_, err := contact.Build(nil, contact.Form{Fields: []contact.Field{{Kind: "custom", Key: "bad key", Value: "x"}}})
	if err == nil {
		t.Fatal("expected an error for a key with a space")
	}
}

func TestFormEmpty(t *testing.T) {
	if !(contact.Form{}).Empty() {
		t.Fatal("zero form should be empty")
	}
	if (contact.Form{Fields: []contact.Field{{Kind: "tel", Value: " "}}}).Empty() != true {
		t.Fatal("whitespace-only value should not count as content")
	}
	if (contact.Form{Name: contact.Name{Given: "A"}}).Empty() {
		t.Fatal("a name should count as content")
	}
	if (contact.Form{Photo: "data:image/png;base64,AA=="}).Empty() {
		t.Fatal("a photo should count as content")
	}
}

func TestBuildNewPhotoV3(t *testing.T) {
	const payload = "iVBORw0KGgo="
	card, err := contact.Build(nil, contact.Form{
		Name:  contact.Name{Given: "Alice"},
		Photo: "data:image/png;base64," + payload,
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	fields := card[vcard.FieldPhoto]
	if len(fields) != 1 {
		t.Fatalf("PHOTO fields = %d, want 1", len(fields))
	}
	if got := fields[0].Value; got != payload {
		t.Fatalf("PHOTO value = %q, want %q", got, payload)
	}
	if got := fields[0].Params.Get("ENCODING"); got != "b" {
		t.Fatalf("ENCODING = %q, want b", got)
	}
	if got := fields[0].Params.Get("TYPE"); got != "PNG" {
		t.Fatalf("TYPE = %q, want PNG", got)
	}
	if got := card.Value(vcard.FieldVersion); got != "3.0" {
		t.Fatalf("VERSION = %q, want 3.0", got)
	}
}

func TestBuildKeepsBasePhotoWithoutNewPhoto(t *testing.T) {
	base := vcard.Card{}
	base.SetValue(vcard.FieldVersion, "3.0")
	base.Set(vcard.FieldPhoto, &vcard.Field{
		Value:  "b2xk",
		Params: vcard.Params{"ENCODING": {"b"}, "TYPE": {"JPEG"}},
	})

	card, err := contact.Build(base, contact.Form{Name: contact.Name{Given: "Alice"}})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if got := card.Value(vcard.FieldPhoto); got != "b2xk" {
		t.Fatalf("PHOTO = %q, want the base photo preserved", got)
	}

	// A second update with no new image must also leave it alone.
	card, err = contact.Build(card, contact.Form{Name: contact.Name{Given: "Alice"}})
	if err != nil {
		t.Fatalf("second build: %v", err)
	}
	if got := card.Value(vcard.FieldPhoto); got != "b2xk" {
		t.Fatalf("PHOTO after second build = %q, want b2xk", got)
	}
}

func TestBuildRemovePhoto(t *testing.T) {
	base := vcard.Card{}
	base.SetValue(vcard.FieldVersion, "3.0")
	base.SetValue(vcard.FieldPhoto, "data:image/png;base64,AAAA")

	card, err := contact.Build(base, contact.Form{
		Name:        contact.Name{Given: "Alice"},
		RemovePhoto: true,
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if fields := card[vcard.FieldPhoto]; len(fields) != 0 {
		t.Fatalf("PHOTO not removed: %v", fields)
	}
}

func TestBuildRejectsInvalidPhoto(t *testing.T) {
	for _, photo := range []string{
		"https://example.com/a.png",
		"data:text/plain;base64,AAAA",
		"data:image/png,AAAA",
		"data:image/png;base64,not base64!",
	} {
		t.Run(photo, func(t *testing.T) {
			_, err := contact.Build(nil, contact.Form{Photo: photo})
			if !errors.Is(err, model.ErrPrecondition) {
				t.Fatalf("err = %v, want ErrPrecondition", err)
			}
		})
	}
}

func TestBuildPhotoV4DataURL(t *testing.T) {
	base := vcard.Card{}
	base.SetValue(vcard.FieldVersion, "4.0")
	const photo = "data:image/webp;base64,UklGRg=="

	card, err := contact.Build(base, contact.Form{
		Name:  contact.Name{Given: "Alice"},
		Photo: photo,
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if got := card.Value(vcard.FieldPhoto); got != photo {
		t.Fatalf("PHOTO = %q, want %q", got, photo)
	}
}
