// Package contact is the structured contact model shared by the admin form and
// the REST API: a card's name parts, property rows, and addresses. Both write
// paths build and read cards through it, so they cannot drift.
package contact

import (
	"sort"
	"strings"

	"github.com/emersion/go-vcard"
	"github.com/nicolaegr/mnemo/internal/model"
	vcardmeta "github.com/nicolaegr/mnemo/internal/vcard"
)

// Kind is one selectable property in the editor, and the only place that maps a
// kind id to its vCard property. "adr" is special: it is offered here but built
// from Address.
type Kind struct {
	ID     string   `json:"id"`
	Key    string   `json:"key"`
	Label  string   `json:"label"`
	Single bool     `json:"single"`
	Types  []string `json:"types,omitempty"`
}

func Kinds() []Kind {
	return []Kind{
		{ID: "tel", Key: vcard.FieldTelephone, Label: "Phone", Types: []string{"home", "work", "cell", "fax"}},
		{ID: "email", Key: vcard.FieldEmail, Label: "Email", Types: []string{"home", "work"}},
		{ID: "url", Key: vcard.FieldURL, Label: "Website", Types: []string{"home", "work"}},
		{ID: "adr", Key: vcard.FieldAddress, Label: "Address", Types: []string{"home", "work"}},
		{ID: "org", Key: vcard.FieldOrganization, Label: "Organization", Single: true},
		{ID: "title", Key: vcard.FieldTitle, Label: "Job title", Single: true},
		{ID: "role", Key: vcard.FieldRole, Label: "Role", Single: true},
		{ID: "bday", Key: vcard.FieldBirthday, Label: "Birthday", Single: true},
		{ID: "anniversary", Key: "ANNIVERSARY", Label: "Anniversary", Single: true},
		{ID: "nickname", Key: vcard.FieldNickname, Label: "Nickname", Single: true},
		{ID: "note", Key: vcard.FieldNote, Label: "Note", Single: true},
		{ID: "impp", Key: "IMPP", Label: "Instant message", Types: []string{"home", "work"}},
		{ID: "categories", Key: vcard.FieldCategories, Label: "Categories", Single: true},
		{ID: "custom", Label: "Custom key"},
	}
}

// KindKey maps a kind to its vCard property; custom has no fixed key.
func KindKey(id string) string {
	for _, k := range Kinds() {
		if k.ID == id {
			return k.Key
		}
	}
	return ""
}

// KindForKey maps a property back to its kind for rendering. ADR is excluded: it
// is handled as Address, not a simple row.
func KindForKey(key string) (string, bool) {
	for _, k := range Kinds() {
		if k.Key != "" && k.Key == key && k.ID != "adr" {
			return k.ID, true
		}
	}
	return "", false
}

// KindSingle reports whether the kind may appear only once.
func KindSingle(id string) bool {
	for _, k := range Kinds() {
		if k.ID == id {
			return k.Single
		}
	}
	return false
}

// PartOption is one optional component of a composable field (a name or address).
type PartOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

func NameParts() []PartOption {
	return []PartOption{
		{ID: "prefix", Label: "Prefix"},
		{ID: "additional", Label: "Middle name"},
		{ID: "suffix", Label: "Suffix"},
	}
}

func AddressParts() []PartOption {
	return []PartOption{
		{ID: "pobox", Label: "PO box"},
		{ID: "ext", Label: "Unit / ext"},
		{ID: "region", Label: "Region"},
		{ID: "postal", Label: "Postal code"},
		{ID: "country", Label: "Country"},
	}
}

// Field is one editable vCard property row. Key is used only for custom rows.
type Field struct {
	Kind  string `json:"kind"`
	Key   string `json:"key,omitempty"`
	Type  string `json:"type,omitempty"`
	Value string `json:"value"`
}

// Address is one ADR, exploded into its vCard components.
type Address struct {
	Type    string `json:"type,omitempty"`
	Pobox   string `json:"pobox,omitempty"`
	Ext     string `json:"ext,omitempty"`
	Street  string `json:"street,omitempty"`
	City    string `json:"city,omitempty"`
	Region  string `json:"region,omitempty"`
	Postal  string `json:"postal,omitempty"`
	Country string `json:"country,omitempty"`
}

func (a Address) empty() bool {
	return a.Pobox == "" && a.Ext == "" && a.Street == "" && a.City == "" &&
		a.Region == "" && a.Postal == "" && a.Country == ""
}

type Name struct {
	Prefix     string `json:"prefix,omitempty"`
	Given      string `json:"given,omitempty"`
	Additional string `json:"additional,omitempty"`
	Family     string `json:"family,omitempty"`
	Suffix     string `json:"suffix,omitempty"`
}

func (n Name) empty() bool {
	return n.Prefix == "" && n.Given == "" && n.Additional == "" && n.Family == "" && n.Suffix == ""
}

// Form is a whole contact in structured form.
type Form struct {
	Name      Name      `json:"name"`
	Fields    []Field   `json:"fields"`
	Addresses []Address `json:"addresses"`
}

// Empty reports whether the form carries no content, so the plane can reject a
// blank create before touching the store.
func (f Form) Empty() bool {
	if !f.Name.empty() {
		return false
	}
	for _, r := range f.Fields {
		if strings.TrimSpace(r.Value) != "" {
			return false
		}
	}
	for _, a := range f.Addresses {
		if !a.empty() {
			return false
		}
	}
	return true
}

// Build renders a Form into a card, keeping the identity and binary properties of
// base (nil for a new contact).
func Build(base vcard.Card, f Form) (vcard.Card, error) {
	card := vcard.Card{}
	for k, fs := range base {
		if preservedKey(k) {
			card[k] = fs
		}
	}
	if card.Value(vcard.FieldVersion) == "" {
		card.SetValue(vcard.FieldVersion, "3.0")
	}
	var existingFN string
	if base != nil {
		existingFN = base.Value(vcard.FieldFormattedName)
	}
	applyName(card, f.Name, existingFN)
	for _, row := range f.Fields {
		key, err := rowKey(row)
		if err != nil {
			return nil, err
		}
		if key == "" {
			continue
		}
		field := &vcard.Field{Value: row.Value}
		if t := strings.TrimSpace(row.Type); t != "" {
			field.Params = vcard.Params{vcard.ParamType: []string{strings.ToUpper(t)}}
		}
		if KindSingle(row.Kind) {
			card.Set(key, field)
		} else {
			card.Add(key, field)
		}
	}
	for _, a := range f.Addresses {
		if a.empty() {
			continue
		}
		field := &vcard.Field{Value: strings.Join([]string{
			a.Pobox, a.Ext, a.Street, a.City, a.Region, a.Postal, a.Country,
		}, ";")}
		if t := strings.TrimSpace(a.Type); t != "" {
			field.Params = vcard.Params{vcard.ParamType: []string{strings.ToUpper(t)}}
		}
		card.Add(vcard.FieldAddress, field)
	}
	vcardmeta.EnsureFormattedName(card)
	return card, nil
}

// Parse reads a card back into a Form, dropping the properties Build preserves.
func Parse(card vcard.Card) Form {
	return Form{
		Name:      parseName(card),
		Fields:    parseFields(card),
		Addresses: parseAddresses(card),
	}
}

func parseName(card vcard.Card) Name {
	n := card.Name()
	if n == nil {
		return Name{}
	}
	return Name{Prefix: n.HonorificPrefix, Given: n.GivenName, Additional: n.AdditionalName, Family: n.FamilyName, Suffix: n.HonorificSuffix}
}

func parseFields(card vcard.Card) []Field {
	if card == nil {
		return nil
	}
	keys := make([]string, 0, len(card))
	for k := range card {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	rows := make([]Field, 0, len(keys))
	for _, k := range keys {
		if preservedKey(k) || k == vcard.FieldFormattedName || k == vcard.FieldName || k == vcard.FieldAddress {
			continue
		}
		for _, f := range card[k] {
			row := Field{Type: strings.ToLower(f.Params.Get(vcard.ParamType)), Value: f.Value}
			if kind, ok := KindForKey(k); ok {
				row.Kind = kind
			} else {
				row.Kind = "custom"
				row.Key = k
			}
			rows = append(rows, row)
		}
	}
	return rows
}

func parseAddresses(card vcard.Card) []Address {
	adrs := card.Addresses()
	out := make([]Address, 0, len(adrs))
	for _, a := range adrs {
		out = append(out, Address{
			Type:    strings.ToLower(a.Params.Get(vcard.ParamType)),
			Pobox:   a.PostOfficeBox,
			Ext:     a.ExtendedAddress,
			Street:  a.StreetAddress,
			City:    a.Locality,
			Region:  a.Region,
			Postal:  a.PostalCode,
			Country: a.Country,
		})
	}
	return out
}

// applyName sets N from the parts and FN from them, keeping the old FN when no
// parts are given (so clearing the name box does not erase an FN a device set).
func applyName(card vcard.Card, n Name, fallbackFN string) {
	if n.empty() {
		if fallbackFN != "" {
			card.SetValue(vcard.FieldFormattedName, fallbackFN)
		}
		return
	}
	card.SetValue(vcard.FieldName, strings.Join([]string{
		n.Family, n.Given, n.Additional, n.Prefix, n.Suffix,
	}, ";"))
	card.SetValue(vcard.FieldFormattedName, vcardmeta.FormattedName(&vcard.Name{
		FamilyName: n.Family, GivenName: n.Given, AdditionalName: n.Additional,
		HonorificPrefix: n.Prefix, HonorificSuffix: n.Suffix,
	}))
}

// rowKey resolves a row to its vCard property; empty means skip the row.
func rowKey(row Field) (string, error) {
	if row.Kind == "custom" {
		key := strings.ToUpper(strings.TrimSpace(row.Key))
		if key == "" {
			return "", nil
		}
		if !validKey(key) {
			return "", model.ErrPrecondition
		}
		return key, nil
	}
	return KindKey(row.Kind), nil
}

func validKey(k string) bool {
	for i := 0; i < len(k); i++ {
		c := k[i]
		if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// preservedKey marks properties the editor never shows or rewrites: identity,
// card version, and binary/informational fields.
func preservedKey(k string) bool {
	switch k {
	case vcard.FieldVersion, vcard.FieldUID, vcard.FieldPhoto, vcard.FieldLogo, vcard.FieldRevision, vcard.FieldSource, "PRODID":
		return true
	}
	return false
}
