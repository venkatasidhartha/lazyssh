package ui

import (
	"reflect"
	"testing"

	"lazyssh/internal/domain"
)

func TestSplitTagInput(t *testing.T) {
	done, cur := splitTagInput("prod, db ,  ,web")
	if !reflect.DeepEqual(done, []string{"prod", "db"}) || cur != "web" {
		t.Fatalf("got %v %q", done, cur)
	}
	done, cur = splitTagInput("prod, ")
	if !reflect.DeepEqual(done, []string{"prod"}) || cur != "" {
		t.Fatalf("got %v %q", done, cur)
	}
}

func TestTagUsageCaseInsensitive(t *testing.T) {
	u := tagUsage([]domain.Server{{Tags: []string{"Prod", "db"}}, {Tags: []string{"prod"}}})
	if u["Prod"] != 2 || u["db"] != 1 || len(u) != 2 {
		t.Fatalf("got %v", u)
	}
}

func TestParseTagsDedupes(t *testing.T) {
	got := parseTags("prod, Prod, db, , kafka, ")
	if !reflect.DeepEqual(got, []string{"prod", "db", "kafka"}) {
		t.Fatalf("got %v", got)
	}
}
