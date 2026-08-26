// Package apptest holds deterministic support code for the end to end suite:
// prompt fixtures and a scripted renderer that replaces the production one when a
// test needs a controlled failure sequence.
package apptest

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/vance1852/manjuflow-studio/internal/apperr"
	"github.com/vance1852/manjuflow-studio/internal/worker"
)

// PromptBody builds a prompt body that satisfies the studio directive contract.
func PromptBody(subject, style string, panels int) string {
	var sb strings.Builder
	sb.WriteString("[subject] ")
	sb.WriteString(subject)
	sb.WriteString("\n[style] ")
	sb.WriteString(style)
	for i := 0; i < panels; i++ {
		fmt.Fprintf(&sb, "\n[panel] beat %d", i+1)
	}
	return sb.String()
}

// Outcome is one scripted render result.
type Outcome struct {
	Artifact string
	Err      error
}

// Fail builds a failing outcome with a stable message.
func Fail(reason string) Outcome {
	return Outcome{Err: apperr.New(apperr.CodeInternal, "%s", reason)}
}

// Succeed builds a successful outcome.
func Succeed(artifact string) Outcome { return Outcome{Artifact: artifact} }

// ScriptedRenderer returns the queued outcomes in order and repeats the last one
// once the script is exhausted.
type ScriptedRenderer struct {
	mu       sync.Mutex
	outcomes []Outcome
	calls    int
	requests []worker.Request
}

// NewScriptedRenderer builds a renderer from an ordered outcome list.
func NewScriptedRenderer(outcomes ...Outcome) *ScriptedRenderer {
	return &ScriptedRenderer{outcomes: outcomes}
}

// Render returns the next scripted outcome.
func (r *ScriptedRenderer) Render(ctx context.Context, request worker.Request) (string, error) {
	if err := apperr.FromContext(ctx); err != nil {
		return "", err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, request)
	index := r.calls
	r.calls++
	if len(r.outcomes) == 0 {
		return "manju://scripted/default", nil
	}
	if index >= len(r.outcomes) {
		index = len(r.outcomes) - 1
	}
	outcome := r.outcomes[index]
	if outcome.Err != nil {
		return "", outcome.Err
	}
	if outcome.Artifact == "" {
		return fmt.Sprintf("manju://scripted/take-%d", index+1), nil
	}
	return outcome.Artifact, nil
}

// Calls reports how often the renderer ran.
func (r *ScriptedRenderer) Calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// Requests returns a copy of the observed requests.
func (r *ScriptedRenderer) Requests() []worker.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]worker.Request, len(r.requests))
	copy(out, r.requests)
	return out
}
