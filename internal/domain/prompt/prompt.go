// Package prompt models the studio prompt library: named templates carrying an
// append only chain of immutable versions.
package prompt

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vance1852/manjuflow-studio/internal/apperr"
)

// VersionStatus is the lifecycle state of one prompt version.
type VersionStatus string

const (
	// StatusDraft is editable by the owner and cannot be bound to a shot.
	StatusDraft VersionStatus = "draft"
	// StatusActive is frozen and may be bound to shots and workshops.
	StatusActive VersionStatus = "active"
	// StatusRetired can no longer be bound. Existing finished references stay
	// readable so historical renders remain explainable.
	StatusRetired VersionStatus = "retired"
)

// Template is a named prompt asset owned by one studio member.
type Template struct {
	ID          int64
	StudioID    int64
	OwnerID     int64
	Slug        string
	Title       string
	Discipline  string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	HeadVersion int
}

// Version is one immutable revision of a template body.
type Version struct {
	ID         int64
	TemplateID int64
	Version    int
	Body       string
	Checksum   string
	Status     VersionStatus
	Notes      string
	CreatedBy  int64
	CreatedAt  time.Time
	RetiredAt  *time.Time
}

const (
	maxBodyRunes  = 8000
	maxTitleRunes = 120
	maxNoteRunes  = 500
)

// NormaliseSlug validates the caller supplied identifier.
func NormaliseSlug(raw string) (string, error) {
	slug := strings.ToLower(strings.TrimSpace(raw))
	if slug == "" {
		return "", apperr.New(apperr.CodeInvalidArgument, "slug must not be empty").With("field", "slug")
	}
	if len(slug) > 64 {
		return "", apperr.New(apperr.CodeInvalidArgument, "slug must not exceed 64 characters").With("field", "slug")
	}
	for _, r := range slug {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
		default:
			return "", apperr.New(apperr.CodeInvalidArgument, "slug accepts lower case letters, digits and hyphen only").
				With("field", "slug")
		}
	}
	if strings.HasPrefix(slug, "-") || strings.HasSuffix(slug, "-") {
		return "", apperr.New(apperr.CodeInvalidArgument, "slug must not start or end with a hyphen").With("field", "slug")
	}
	return slug, nil
}

// ValidateTitle enforces the display title bounds.
func ValidateTitle(raw string) (string, error) {
	title := strings.TrimSpace(raw)
	if title == "" {
		return "", apperr.New(apperr.CodeInvalidArgument, "title must not be empty").With("field", "title")
	}
	if utf8.RuneCountInString(title) > maxTitleRunes {
		return "", apperr.New(apperr.CodeInvalidArgument, "title must not exceed %d characters", maxTitleRunes).
			With("field", "title")
	}
	return title, nil
}

// ValidateBody enforces the prompt body contract. A usable comic drama prompt
// must address at least the shot subject and the visual style, which the studio
// encodes as two required directive labels.
func ValidateBody(raw string) (string, error) {
	body := strings.TrimSpace(raw)
	if body == "" {
		return "", apperr.New(apperr.CodeInvalidArgument, "prompt body must not be empty").With("field", "body")
	}
	if utf8.RuneCountInString(body) > maxBodyRunes {
		return "", apperr.New(apperr.CodeInvalidArgument, "prompt body must not exceed %d characters", maxBodyRunes).
			With("field", "body")
	}
	for _, directive := range RequiredDirectives() {
		if !strings.Contains(body, directive) {
			return "", apperr.New(apperr.CodeInvalidArgument, "prompt body must declare %s", directive).
				With("field", "body").
				With("missing_directive", directive)
		}
	}
	return body, nil
}

// RequiredDirectives returns an isolated copy of the mandatory directive labels.
func RequiredDirectives() []string {
	return []string{"[subject]", "[style]"}
}

// ValidateNotes bounds the free form revision note.
func ValidateNotes(raw string) (string, error) {
	notes := strings.TrimSpace(raw)
	if utf8.RuneCountInString(notes) > maxNoteRunes {
		return "", apperr.New(apperr.CodeInvalidArgument, "notes must not exceed %d characters", maxNoteRunes).
			With("field", "notes")
	}
	return notes, nil
}

// Checksum digests a prompt body so identical revisions can be detected.
func Checksum(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// Activate freezes a draft version.
func (v *Version) Activate(now time.Time) error {
	switch v.Status {
	case StatusActive:
		return apperr.New(apperr.CodeConflict, "version %d is already active", v.Version).
			With("prompt_version", v.Checksum)
	case StatusRetired:
		return apperr.New(apperr.CodeFailedPrecondition, "retired version %d cannot be activated", v.Version)
	}
	v.Status = StatusActive
	v.RetiredAt = nil
	_ = now
	return nil
}

// Retire withdraws an active version. Live references must be released first so
// running renders and open workshops never lose their source of truth.
func (v *Version) Retire(now time.Time, liveReferences int) error {
	if v.Status == StatusRetired {
		return apperr.New(apperr.CodeConflict, "version %d is already retired", v.Version)
	}
	if liveReferences > 0 {
		return apperr.New(apperr.CodeFailedPrecondition,
			"version %d still backs %d live references", v.Version, liveReferences).
			With("live_references", strconv.Itoa(liveReferences))
	}
	v.Status = StatusRetired
	retiredAt := now
	v.RetiredAt = &retiredAt
	return nil
}

// RetireSuperseded withdraws a version that a newer activation replaced. The
// studio keeps one version in use per template, so the previous one steps aside.
func (v *Version) RetireSuperseded(now time.Time) error {
	if v.Status != StatusActive {
		return nil
	}
	v.Status = StatusRetired
	retiredAt := now
	v.RetiredAt = &retiredAt
	v.Notes = strings.TrimSpace(v.Notes + " 已被新激活的版本替代")
	return nil
}

// EnsureBindable rejects versions that must not enter production.
func (v *Version) EnsureBindable() error {
	switch v.Status {
	case StatusActive:
		return nil
	case StatusDraft:
		return apperr.New(apperr.CodeFailedPrecondition, "version %d is still a draft", v.Version).
			With("prompt_version_status", string(v.Status))
	default:
		return apperr.New(apperr.CodeFailedPrecondition, "version %d is retired", v.Version).
			With("prompt_version_status", string(v.Status))
	}
}

// VerifyIntegrity detects a body that no longer matches its stored checksum.
func (v *Version) VerifyIntegrity() error {
	if Checksum(v.Body) != v.Checksum {
		return apperr.New(apperr.CodeInternal, "prompt version %d failed its checksum", v.Version).
			With("template_id", strconv.FormatInt(v.TemplateID, 10))
	}
	return nil
}

// Clone returns a value copy so repository results cannot be mutated through a
// shared pointer.
func (v Version) Clone() Version {
	if v.RetiredAt != nil {
		retired := *v.RetiredAt
		v.RetiredAt = &retired
	}
	return v
}
