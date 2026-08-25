package repository

import (
	"testing"

	"github.com/vance1852/manjuflow-studio/internal/apperr"
)

var allowed = map[string]string{
	"created_at": "created_at",
	"code":       "code",
}

func TestNormalisePageAppliesDefaults(t *testing.T) {
	page, err := NormalisePage(Page{}, allowed, "created_at")
	if err != nil {
		t.Fatalf("an empty page was rejected: %v", err)
	}
	if page.Limit != DefaultLimit {
		t.Fatalf("default limit is %d", page.Limit)
	}
	if page.SortBy != "created_at" {
		t.Fatalf("default sort key is %q", page.SortBy)
	}
	if page.Desc {
		t.Fatal("the default order is descending")
	}
}

func TestNormalisePageRejectsUnsupportedInput(t *testing.T) {
	if _, err := NormalisePage(Page{Limit: -1}, allowed, "created_at"); !apperr.IsCode(err, apperr.CodeInvalidArgument) {
		t.Fatalf("a negative limit reported %v", apperr.CodeOf(err))
	}
	if _, err := NormalisePage(Page{Limit: MaxLimit + 1}, allowed, "created_at"); err == nil {
		t.Fatal("an oversized limit was accepted")
	}
	if _, err := NormalisePage(Page{Offset: -5}, allowed, "created_at"); err == nil {
		t.Fatal("a negative offset was accepted")
	}
	if _, err := NormalisePage(Page{SortBy: "password"}, allowed, "created_at"); err == nil {
		t.Fatal("an unlisted sort key was accepted")
	}
}

func TestNormalisePageAcceptsListedSortKeys(t *testing.T) {
	page, err := NormalisePage(Page{Limit: 5, Offset: 10, SortBy: " CODE ", Desc: true}, allowed, "created_at")
	if err != nil {
		t.Fatalf("a listed sort key was rejected: %v", err)
	}
	if page.SortBy != "code" {
		t.Fatalf("sort key normalised to %q", page.SortBy)
	}
	if Column(allowed, page.SortBy) != "code" {
		t.Fatalf("column resolution returned %q", Column(allowed, page.SortBy))
	}
	if page.Limit != 5 || page.Offset != 10 || !page.Desc {
		t.Fatalf("page fields were altered: %#v", page)
	}
}

func TestQuotaRemainingNeverGoesNegative(t *testing.T) {
	if got := (Quota{Capacity: 5, Used: 2}).Remaining(); got != 3 {
		t.Fatalf("remaining is %d, want 3", got)
	}
	if got := (Quota{Capacity: 5, Used: 5}).Remaining(); got != 0 {
		t.Fatalf("an exhausted quota reports %d", got)
	}
	if got := (Quota{Capacity: 5, Used: 9}).Remaining(); got != 0 {
		t.Fatalf("an overdrawn quota reports %d", got)
	}
}

func TestNotFoundCarriesTheEntityAndKey(t *testing.T) {
	err := NotFound("series", int64(42))
	if !apperr.IsCode(err, apperr.CodeNotFound) {
		t.Fatalf("NotFound produced %v", apperr.CodeOf(err))
	}
	typed, ok := apperr.As(err)
	if !ok {
		t.Fatal("NotFound is not classified")
	}
	if typed.Details["entity"] != "series" || typed.Details["key"] != "42" {
		t.Fatalf("details are %#v", typed.Details)
	}
	textual, _ := apperr.As(NotFound("prompt template", "night-market"))
	if textual.Details["key"] != "night-market" {
		t.Fatalf("textual key is %q", textual.Details["key"])
	}
	unknown, _ := apperr.As(NotFound("shot", 3.5))
	if unknown.Details["key"] != "unspecified" {
		t.Fatalf("unsupported key type produced %q", unknown.Details["key"])
	}
}
