// Package audit defines the immutable audit event written inside the same
// transaction as the business change it describes.
package audit

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/vance1852/manjuflow-studio/internal/apperr"
)

// Result classifies the outcome recorded by an audit event.
type Result string

const (
	// ResultSuccess records a committed business change.
	ResultSuccess Result = "success"
	// ResultRejected records a business rule that refused the request.
	ResultRejected Result = "rejected"
)

// Object types touched by audited actions.
const (
	ObjectPromptVersion = "prompt_version"
	ObjectSeries        = "series"
	ObjectShot          = "shot"
	ObjectRenderJob     = "render_job"
	ObjectWorkshop      = "workshop"
	ObjectEnrollment    = "enrollment"
	ObjectSubmission    = "practice_submission"
	ObjectSession       = "session"
)

// Event is one audit record.
type Event struct {
	ID         int64
	StudioID   int64
	ActorID    int64
	ActorRole  string
	Action     string
	ObjectType string
	ObjectID   int64
	Result     Result
	RequestID  string
	Detail     map[string]string
	CreatedAt  time.Time
}

// Validate rejects incomplete events so the trail stays queryable.
func (e Event) Validate() error {
	if strings.TrimSpace(e.Action) == "" {
		return apperr.New(apperr.CodeInternal, "audit event needs an action")
	}
	if strings.TrimSpace(e.ObjectType) == "" {
		return apperr.New(apperr.CodeInternal, "audit event needs an object type")
	}
	if e.StudioID == 0 {
		return apperr.New(apperr.CodeInternal, "audit event needs a studio")
	}
	if e.Result != ResultSuccess && e.Result != ResultRejected {
		return apperr.New(apperr.CodeInternal, "audit event has an unknown result %q", string(e.Result))
	}
	return nil
}

// WithDetail returns a copy carrying one more detail pair. The receiver is never
// mutated so callers can safely reuse a template event.
func (e Event) WithDetail(key, value string) Event {
	clone := e.Clone()
	if clone.Detail == nil {
		clone.Detail = map[string]string{}
	}
	clone.Detail[key] = value
	return clone
}

// Clone returns a deep copy including the detail map.
func (e Event) Clone() Event {
	copied := e
	if e.Detail != nil {
		copied.Detail = make(map[string]string, len(e.Detail))
		for key, value := range e.Detail {
			copied.Detail[key] = value
		}
	}
	return copied
}

// DetailJSON serialises the detail map with deterministic key ordering so audit
// rows can be compared byte for byte across runs.
func (e Event) DetailJSON() (string, error) {
	if len(e.Detail) == 0 {
		return "{}", nil
	}
	keys := make([]string, 0, len(e.Detail))
	for key := range e.Detail {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var sb strings.Builder
	sb.WriteByte('{')
	for i, key := range keys {
		if i > 0 {
			sb.WriteByte(',')
		}
		encodedKey, err := json.Marshal(key)
		if err != nil {
			return "", apperr.Wrap(err, apperr.CodeInternal, "cannot encode audit detail key")
		}
		encodedValue, err := json.Marshal(e.Detail[key])
		if err != nil {
			return "", apperr.Wrap(err, apperr.CodeInternal, "cannot encode audit detail value")
		}
		sb.Write(encodedKey)
		sb.WriteByte(':')
		sb.Write(encodedValue)
	}
	sb.WriteByte('}')
	return sb.String(), nil
}

// ParseDetail restores a detail map from its stored JSON form.
func ParseDetail(raw string) (map[string]string, error) {
	if strings.TrimSpace(raw) == "" {
		return map[string]string{}, nil
	}
	detail := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &detail); err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "stored audit detail is not decodable")
	}
	return detail, nil
}
