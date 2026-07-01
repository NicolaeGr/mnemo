package pages

import (
	"context"
	"io"

	"github.com/a-h/templ"
)

func VCardTypeOptions(selected string) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		groups := []struct {
			Label  string
			Fields []struct{ Value, Label string }
		}{
			{"Contact", []struct{ Value, Label string }{
				{"TEL", "Phone"}, {"EMAIL", "Email"}, {"IMPP", "IM/Chat"}, {"URL", "Website"},
			}},
			{"Personal", []struct{ Value, Label string }{
				{"NICKNAME", "Nickname"}, {"BDAY", "Birthday"}, {"ANNIVERSARY", "Anniversary"},
				{"GENDER", "Gender"}, {"LANG", "Language"}, {"TZ", "Time Zone"},
			}},
			{"Work", []struct{ Value, Label string }{
				{"ROLE", "Role"}, {"ORG", "Organization"}, {"TITLE", "Title"},
				{"LOGO", "Logo"}, {"KEY", "Public Key"},
			}},
			{"Address", []struct{ Value, Label string }{
				{"ADR", "Address"}, {"GEO", "Geo Location"},
			}},
			{"Relationships", []struct{ Value, Label string }{
				{"RELATED", "Related"}, {"MEMBER", "Group Member"},
			}},
			{"IDs & Calendar", []struct{ Value, Label string }{
				{"UID", "UID"}, {"PRODID", "Product ID"}, {"CALURI", "Calendar URI"},
				{"FBURL", "Free/Busy URL"}, {"CATEGORIES", "Categories"},
				{"NOTE", "Note"}, {"SOUND", "Sound"},
			}},
		}

		for _, g := range groups {
			io.WriteString(w, `<optgroup label="`+g.Label+`">`)
			for _, f := range g.Fields {
				sel := ""
				if f.Value == selected {
					sel = " selected"
				}
				io.WriteString(w, `<option value="`+f.Value+`"`+sel+`>`+f.Label+`</option>`)
			}
			io.WriteString(w, `</optgroup>`)
		}
		return nil
	})
}
