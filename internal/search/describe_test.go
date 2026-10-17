package search

import (
	"testing"
	"time"
)

func TestDescribe(t *testing.T) {
	schema := testSchema()
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"josh", "josh"},
		{`"josh smith"`, "josh smith"},
		{"name:josh", "name containing josh"},
		{"name:jo*", "name starting with jo"},
		{`name="Josh Smith"`, "name is Josh Smith"},
		{"org:acme", "organization containing acme"},
		{"has:phone", "with a phone"},
		{"has:email", "with an email"},
		{"has:org", "with an organization"},
		{"has:*", "with a phone, an email, or an organization"},
		{"no has:phone", "with no phone"},
		{"no has:email", "with no email"},
		{"no has:org", "with no organization"},
		{"untagged:true", "untagged"},
		{"untagged:false", "in a list"},
		{"no untagged:true", "in a list"},
		{"email:*", "with an email"},
		{"no email:*", "without an email"},
		{"in:Work", "in the list Work"},
		{`in:"Family Reunion"`, "in the list Family Reunion"},
		{"no in:Work", "not in the list Work"},
		{"no name:josh", "name not containing josh"},
		{"josh smith", "josh and smith"},
		{"name:alice or name:bob", "name containing alice or name containing bob"},
		{"(org:acme or org:beta) no has:email", "(organization containing acme or organization containing beta) and with no email"},
		{"no (has:phone or has:email)", "not (with a phone or with an email)"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := Describe(Parse(tc.in, schema).Expr, schema, nil)
			if got != tc.want {
				t.Fatalf("Describe(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestDescribePlanned(t *testing.T) {
	schema := testSchema()
	now := time.Now().UTC()

	cases := []struct {
		name string
		expr Expr
		want string
	}{
		{
			name: "last seven days",
			expr: Term{Field: "created", Op: OpContains, Value: TimeRange{From: now.Add(-7 * 24 * time.Hour), To: now}},
			want: "created in the last 7 days",
		},
		{
			name: "last year",
			expr: Term{Field: "updated", Op: OpContains, Value: TimeRange{From: now.Add(-365 * 24 * time.Hour), To: now}},
			want: "updated in the last year",
		},
		{
			name: "day range",
			expr: Term{Field: "created", Op: OpEqual, Value: TimeRange{
				From: time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC),
				To:   time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC),
			}},
			want: "created on 2024-01-02",
		},
		{
			name: "not older than a week",
			expr: Not{Kid: Term{Field: "updated", Op: OpGt, Value: Time(now.Add(-7 * 24 * time.Hour))}},
			want: "updated more than a week ago",
		},
		{
			name: "named list",
			expr: Term{Field: "in", Op: OpEqual, Value: IDs{1}},
			want: "in the list Work",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			namer := ListNamer(func(ids []int64) string {
				if len(ids) == 1 && ids[0] == 1 {
					return "the list Work"
				}
				return ""
			})
			got := Describe(tc.expr, schema, namer)
			if got != tc.want {
				t.Fatalf("Describe = %q, want %q", got, tc.want)
			}
		})
	}
}
