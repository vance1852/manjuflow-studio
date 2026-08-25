package prompt

import (
	"strings"
	"testing"
	"time"

	"github.com/vance1852/manjuflow-studio/internal/apperr"
)

func stamp() time.Time { return time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC) }

func validBody() string {
	return "[subject] 夜市追逐的女主角\n[style] 赛博水墨\n[panel] 起势"
}

func TestSlugNormalisationAcceptsOnlyTheAllowedShape(t *testing.T) {
	slug, err := NormaliseSlug("  Night-Market-01 ")
	if err != nil {
		t.Fatalf("a valid slug was refused: %v", err)
	}
	if slug != "night-market-01" {
		t.Fatalf("slug was normalised to %q", slug)
	}
	for _, candidate := range []string{"", "-leading", "trailing-", "with space", "under_score", strings.Repeat("a", 65)} {
		if _, err := NormaliseSlug(candidate); !apperr.IsCode(err, apperr.CodeInvalidArgument) {
			t.Fatalf("slug %q reported %v", candidate, apperr.CodeOf(err))
		}
	}
}

func TestBodyMustDeclareEveryRequiredDirective(t *testing.T) {
	if _, err := ValidateBody("   "); !apperr.IsCode(err, apperr.CodeInvalidArgument) {
		t.Fatalf("empty body reported %v", apperr.CodeOf(err))
	}
	if _, err := ValidateBody("[subject] 主角"); err == nil {
		t.Fatal("a body without a style directive was accepted")
	}
	if _, err := ValidateBody("[style] 水墨"); err == nil {
		t.Fatal("a body without a subject directive was accepted")
	}
	body, err := ValidateBody("  " + validBody() + "  ")
	if err != nil {
		t.Fatalf("a valid body was refused: %v", err)
	}
	if strings.HasPrefix(body, " ") || strings.HasSuffix(body, " ") {
		t.Fatal("the body was not trimmed")
	}
	oversized := validBody() + strings.Repeat("甲", 8001)
	if _, err := ValidateBody(oversized); err == nil {
		t.Fatal("an oversized body was accepted")
	}
}

func TestRequiredDirectivesAreReturnedAsACopy(t *testing.T) {
	first := RequiredDirectives()
	if len(first) < 2 {
		t.Fatalf("only %d directives are required", len(first))
	}
	first[0] = "[tampered]"
	second := RequiredDirectives()
	if second[0] == "[tampered]" {
		t.Fatal("the directive list is shared between callers")
	}
}

func TestTitleAndNotesBounds(t *testing.T) {
	if _, err := ValidateTitle("  "); err == nil {
		t.Fatal("a blank title was accepted")
	}
	if _, err := ValidateTitle(strings.Repeat("甲", 121)); err == nil {
		t.Fatal("an oversized title was accepted")
	}
	if _, err := ValidateNotes(strings.Repeat("乙", 501)); err == nil {
		t.Fatal("oversized notes were accepted")
	}
	notes, err := ValidateNotes("  初版  ")
	if err != nil || notes != "初版" {
		t.Fatalf("notes were normalised to %q with %v", notes, err)
	}
}

func TestActivationRefusesRetiredVersionsAndRepeats(t *testing.T) {
	version := &Version{Version: 1, Body: validBody(), Checksum: Checksum(validBody()), Status: StatusDraft}
	if err := version.Activate(stamp()); err != nil {
		t.Fatalf("activation was refused: %v", err)
	}
	if version.Status != StatusActive {
		t.Fatalf("version status is %s", version.Status)
	}
	if err := version.Activate(stamp()); !apperr.IsCode(err, apperr.CodeConflict) {
		t.Fatalf("repeated activation reported %v", apperr.CodeOf(err))
	}
	retired := &Version{Version: 2, Status: StatusRetired}
	if err := retired.Activate(stamp()); !apperr.IsCode(err, apperr.CodeFailedPrecondition) {
		t.Fatalf("activating a retired version reported %v", apperr.CodeOf(err))
	}
}

func TestRetirementIsBlockedByLiveReferences(t *testing.T) {
	version := &Version{Version: 3, Status: StatusActive}
	err := version.Retire(stamp(), 2)
	if !apperr.IsCode(err, apperr.CodeFailedPrecondition) {
		t.Fatalf("blocked retirement reported %v", apperr.CodeOf(err))
	}
	typed, ok := apperr.As(err)
	if !ok || typed.Details["live_references"] != "2" {
		t.Fatalf("refusal does not report the reference count: %#v", err)
	}
	if version.Status != StatusActive {
		t.Fatalf("blocked retirement changed the status to %s", version.Status)
	}
	if err := version.Retire(stamp(), 0); err != nil {
		t.Fatalf("retirement was refused without references: %v", err)
	}
	if version.RetiredAt == nil || !version.RetiredAt.Equal(stamp()) {
		t.Fatalf("retirement instant is %v", version.RetiredAt)
	}
	if err := version.Retire(stamp(), 0); !apperr.IsCode(err, apperr.CodeConflict) {
		t.Fatalf("repeated retirement reported %v", apperr.CodeOf(err))
	}
}

func TestOnlyActiveVersionsAreBindable(t *testing.T) {
	active := &Version{Version: 1, Status: StatusActive}
	if err := active.EnsureBindable(); err != nil {
		t.Fatalf("an active version was refused: %v", err)
	}
	draft := &Version{Version: 2, Status: StatusDraft}
	if err := draft.EnsureBindable(); !apperr.IsCode(err, apperr.CodeFailedPrecondition) {
		t.Fatalf("a draft version reported %v", apperr.CodeOf(err))
	}
	retired := &Version{Version: 3, Status: StatusRetired}
	if err := retired.EnsureBindable(); !apperr.IsCode(err, apperr.CodeFailedPrecondition) {
		t.Fatalf("a retired version reported %v", apperr.CodeOf(err))
	}
}

func TestChecksumDetectsATamperedBody(t *testing.T) {
	body := validBody()
	version := &Version{Version: 1, Body: body, Checksum: Checksum(body)}
	if err := version.VerifyIntegrity(); err != nil {
		t.Fatalf("an intact version failed its checksum: %v", err)
	}
	version.Body = body + " 被改动"
	if err := version.VerifyIntegrity(); !apperr.IsCode(err, apperr.CodeInternal) {
		t.Fatalf("a tampered body reported %v", apperr.CodeOf(err))
	}
	if Checksum("a") == Checksum("b") {
		t.Fatal("different bodies share a checksum")
	}
	if Checksum(body) != Checksum(body) {
		t.Fatal("the checksum is not stable")
	}
}

func TestVersionCloneIsolatesTheRetirementInstant(t *testing.T) {
	version := &Version{Version: 1, Status: StatusActive}
	if err := version.Retire(stamp(), 0); err != nil {
		t.Fatalf("retirement was refused: %v", err)
	}
	clone := version.Clone()
	*clone.RetiredAt = stamp().Add(time.Hour)
	if !version.RetiredAt.Equal(stamp()) {
		t.Fatal("clone shares the retirement instant")
	}
}
