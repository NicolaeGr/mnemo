package contact_test

import (
	"strings"
	"testing"

	"github.com/emersion/go-vcard"

	"github.com/nicolaegr/mnemo/internal/contact"
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
}
