package store

import (
	"reflect"
	"testing"
)

func TestParseQuery(t *testing.T) {
	cases := []struct {
		in   string
		want []queryTerm
	}{
		{"", nil},
		{"   ", nil},
		{"Josh", []queryTerm{{value: "Josh"}}},
		{"name:Josh", []queryTerm{{field: "name", value: "Josh"}}},
		{"name=Josh", []queryTerm{{field: "name", value: "Josh"}}},
		{"Name:Josh", []queryTerm{{field: "name", value: "Josh"}}},
		{`org:"Acme Inc"`, []queryTerm{{field: "org", value: "Acme Inc"}}},
		// Spaces around the separator are tolerated.
		{`name : "Josh Smith"`, []queryTerm{{field: "name", value: "Josh Smith"}}},
		{"name= Josh", []queryTerm{{field: "name", value: "Josh"}}},
		{"org :Acme", []queryTerm{{field: "org", value: "Acme"}}},
		{"name:Josh org:Acme", []queryTerm{{field: "name", value: "Josh"}, {field: "org", value: "Acme"}}},
		{`"Josh Smith"`, []queryTerm{{value: "Josh Smith"}}},
		{"number:399", []queryTerm{{field: "number", value: "399"}}},
		{"untagged:1", []queryTerm{{field: "untagged", value: "1"}}},
		// A known key without a separator stays free text.
		{"has phone", []queryTerm{{value: "has"}, {value: "phone"}}},
		// An unknown key is left as free text (and a lone separator is dropped).
		{"foo=bar", []queryTerm{{value: "foo=bar"}}},
		{"foo : bar", []queryTerm{{value: "foo"}, {value: "bar"}}},
		{"  spaced   out  ", []queryTerm{{value: "spaced"}, {value: "out"}}},
	}
	for _, tc := range cases {
		if got := parseQuery(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("parseQuery(%q) = %#v, want %#v", tc.in, got, tc.want)
		}
	}
}
