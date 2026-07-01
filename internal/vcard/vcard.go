package vcard

import "strings"

type VCard struct {
	DisplayName string
	Name        string
	Org         string
	Title       string
	Note        string
	Phones      []*TypedField
	Emails      []*TypedField
	Addresses   []*Address
	Photo       string
	PhotoType   string
	Extra       []*Extra
	RawText     string
}

type TypedField struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type Address struct {
	Type       string `json:"type"`
	Street     string `json:"street"`
	City       string `json:"city"`
	Region     string `json:"region"`
	PostalCode string `json:"postal_code"`
	Country    string `json:"country"`
}

type Extra struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func skipProp(name string) bool {
	switch name {
	case "VERSION", "PRODID", "REV", "UID", "KIND", "SOURCE",
		"BEGIN", "END", "CALURI", "CALADRURI", "FBURL",
		"PROFILE", "NAME", "MAILER":
		return true
	default:
		return false
	}
}

func Parse(text string) *VCard {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	v := &VCard{RawText: text}
	if text == "" {
		return v
	}

	lines := splitLines(text)
	for _, line := range lines {
		if line == "" {
			continue
		}
		prop, params, value := splitLine(line)
		value = unescape(value)

		switch prop {
		case "FN":
			v.DisplayName = value
		case "N":
			v.Name = value
		case "ORG":
			v.Org = value
		case "TITLE":
			v.Title = value
		case "NOTE":
			v.Note = value
		case "TEL":
			v.Phones = append(v.Phones, &TypedField{Type: typeFromParams(params, "VOICE"), Value: value})
		case "EMAIL":
			v.Emails = append(v.Emails, &TypedField{Type: typeFromParams(params, "INTERNET"), Value: value})
		case "ADR":
			v.Addresses = append(v.Addresses, parseAddress(params, value))
		case "PHOTO":
			if value != "" {
				v.Photo = value
				for _, p := range params {
					if len(p) > 5 && p[:5] == "TYPE=" {
						v.PhotoType = p[5:]
					}
				}
			}
		default:
			if !skipProp(prop) && prop != "" && value != "" {
				v.Extra = append(v.Extra, &Extra{Name: prop, Value: value})
			}
		}
	}
	return v
}

func Generate(v *VCard) string {
	s := "BEGIN:VCARD\nVERSION:3.0\n"
	s += "FN:" + escape(v.DisplayName) + "\n"
	s += "N:" + escape(v.Name) + "\n"

	for _, p := range v.Phones {
		s += "TEL;TYPE=" + p.Type + ":" + p.Value + "\n"
	}
	for _, e := range v.Emails {
		s += "EMAIL;TYPE=" + e.Type + ":" + e.Value + "\n"
	}
	for _, a := range v.Addresses {
		s += "ADR;TYPE=" + a.Type + ":" + buildADR(a) + "\n"
	}

	if v.Org != "" {
		s += "ORG:" + escape(v.Org) + "\n"
	}
	if v.Title != "" {
		s += "TITLE:" + escape(v.Title) + "\n"
	}
	if v.Photo != "" {
		s += "PHOTO;ENCODING=b;TYPE=" + v.PhotoType + ":" + v.Photo + "\n"
	}
	if v.Note != "" {
		s += "NOTE:" + escape(v.Note) + "\n"
	}
	for _, e := range v.Extra {
		s += e.Name + ":" + escape(e.Value) + "\n"
	}

	s += "END:VCARD\n"
	return s
}

func splitLines(text string) []string {
	var lines []string
	current := ""
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c == '\n' || c == '\r' {
			if current != "" {

				if i+1 < len(text) && (text[i+1] == ' ' || text[i+1] == '\t') {
					i++
					continue
				}
				lines = append(lines, current)
				current = ""
			}
			if c == '\r' && i+1 < len(text) && text[i+1] == '\n' {
				i++
			}
		} else {
			current += string(c)
		}
	}
	if current != "" {
		lines = append(lines, current)
	}
	return lines
}

func splitLine(line string) (prop string, params []string, value string) {
	colon := -1
	for i, c := range line {
		if c == ':' {
			colon = i
			break
		}
	}
	if colon < 0 {
		return line, nil, ""
	}

	head := line[:colon]
	value = line[colon+1:]

	parts := splitSemicolons(head)
	prop = parts[0]
	if len(parts) > 1 {
		params = parts[1:]
	}
	return prop, params, value
}

func splitSemicolons(s string) []string {
	var parts []string
	current := ""
	for i := 0; i < len(s); i++ {
		if s[i] == ';' {
			parts = append(parts, current)
			current = ""
		} else {
			current += string(s[i])
		}
	}
	parts = append(parts, current)
	return parts
}

func typeFromParams(params []string, defaultType string) string {
	for _, p := range params {
		if len(p) > 5 && p[:5] == "TYPE=" {
			return p[5:]
		}
	}
	return defaultType
}

func parseAddress(params []string, value string) *Address {
	parts := splitSemicolons(value)
	a := &Address{Type: typeFromParams(params, "HOME")}
	if len(parts) > 2 {
		a.Street = parts[2]
	}
	if len(parts) > 3 {
		a.City = parts[3]
	}
	if len(parts) > 4 {
		a.Region = parts[4]
	}
	if len(parts) > 5 {
		a.PostalCode = parts[5]
	}
	if len(parts) > 6 {
		a.Country = parts[6]
	}
	return a
}

func buildADR(a *Address) string {
	return ";;;" + a.City + ";" + a.Region + ";" + a.PostalCode + ";" + a.Country
}

func escape(s string) string {
	res := ""
	for _, c := range s {
		switch c {
		case ';':
			res += "\\;"
		case ',':
			res += "\\,"
		case '\n':
			res += "\\n"
		default:
			res += string(c)
		}
	}
	return res
}

func unescape(s string) string {
	res := ""
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case 'n', 'N':
				res += "\n"
			case ';':
				res += ";"
			case ',':
				res += ","
			case '\\':
				res += "\\"
			default:
				res += string(s[i+1])
			}
			i++
		} else {
			res += string(s[i])
		}
	}
	return res
}

func NewVCard(displayName string) *VCard {
	return &VCard{
		DisplayName: displayName,
		Name:        ";" + displayName + ";;;",
	}
}

func (v *VCard) String() string {
	return Generate(v)
}

type FormData struct {
	FirstName, LastName, MiddleName, Prefix, Suffix string
	DisplayName, Org, Title, Note                   string
	PhotoB64, PhotoType                             string
	PhoneTypes, PhoneVals                           []string
	EmailTypes, EmailVals                           []string
	ExtraNames, ExtraValues                         []string
}

func FromForm(d *FormData) *VCard {
	n := d.LastName + ";" + d.FirstName + ";" + d.MiddleName + ";" + d.Prefix + ";" + d.Suffix
	displayName := d.DisplayName
	if displayName == "" {
		displayName = ComposeDisplayName(n, "")
		if displayName == "" {
			displayName = "Unknown"
		}
	}

	vc := &VCard{
		DisplayName: displayName,
		Name:        n,
		Org:         d.Org,
		Title:       d.Title,
		Note:        d.Note,
	}

	if d.PhotoB64 != "" {
		vc.Photo = d.PhotoB64
		vc.PhotoType = d.PhotoType
		if vc.PhotoType == "" {
			vc.PhotoType = "image/jpeg"
		}
	}

	for i := range d.PhoneTypes {
		if i < len(d.PhoneVals) && d.PhoneVals[i] != "" {
			vc.Phones = append(vc.Phones, &TypedField{Type: d.PhoneTypes[i], Value: d.PhoneVals[i]})
		}
	}
	for i := range d.EmailTypes {
		if i < len(d.EmailVals) && d.EmailVals[i] != "" {
			vc.Emails = append(vc.Emails, &TypedField{Type: d.EmailTypes[i], Value: d.EmailVals[i]})
		}
	}
	for i := range d.ExtraNames {
		if i < len(d.ExtraValues) && d.ExtraNames[i] != "" {
			vc.Extra = append(vc.Extra, &Extra{Name: d.ExtraNames[i], Value: d.ExtraValues[i]})
		}
	}
	return vc
}

func ParseFirstName(n string) string {
	parts := splitSemicolons(n)
	if len(parts) >= 2 {
		return parts[1]
	}
	return ""
}

func ParseLastName(n string) string {
	parts := splitSemicolons(n)
	if len(parts) >= 1 {
		return parts[0]
	}
	return ""
}

func ParseMiddleName(n string) string {
	parts := splitSemicolons(n)
	if len(parts) >= 3 {
		return parts[2]
	}
	return ""
}

func ParsePrefix(n string) string {
	parts := splitSemicolons(n)
	if len(parts) >= 4 {
		return parts[3]
	}
	return ""
}

func ParseSuffix(n string) string {
	parts := splitSemicolons(n)
	if len(parts) >= 5 {
		return parts[4]
	}
	return ""
}

// Builds display name from N property parts. Placeholders: {first}, {last}, {middle}, {prefix}, {suffix}. Default: "{first} {last}"
func ComposeDisplayName(n, format string) string {
	if format == "" {
		format = "{first} {last}"
	}
	first := ParseFirstName(n)
	last := ParseLastName(n)
	middle := ParseMiddleName(n)
	prefix := ParsePrefix(n)
	suffix := ParseSuffix(n)

	result := format
	result = strings.ReplaceAll(result, "{first}", first)
	result = strings.ReplaceAll(result, "{last}", last)
	result = strings.ReplaceAll(result, "{middle}", middle)
	result = strings.ReplaceAll(result, "{prefix}", prefix)
	result = strings.ReplaceAll(result, "{suffix}", suffix)

	result = strings.Join(strings.Fields(result), " ")
	return result
}

func PhotoURL(v *VCard) string {
	if v.Photo == "" {
		return ""
	}
	if strings.HasPrefix(v.Photo, "data:") {
		return v.Photo
	}
	pt := v.PhotoType
	if pt == "" {
		pt = "image/jpeg"
	}
	return "data:" + pt + ";base64," + v.Photo
}
