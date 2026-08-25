package apperr

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestCodeOfClassifiesContextFailuresBeforeInternal(t *testing.T) {
	if CodeOf(nil) != "" {
		t.Fatal("a nil error produced a code")
	}
	if got := CodeOf(context.Canceled); got != CodeCanceled {
		t.Fatalf("cancellation classified as %s", got)
	}
	if got := CodeOf(context.DeadlineExceeded); got != CodeDeadlineExceeded {
		t.Fatalf("deadline classified as %s", got)
	}
	wrapped := Wrap(context.Canceled, CodeInternal, "database call failed")
	if got := CodeOf(wrapped); got != CodeCanceled {
		t.Fatalf("a wrapped cancellation classified as %s, want the context code", got)
	}
	if got := CodeOf(errors.New("boom")); got != CodeInternal {
		t.Fatalf("an unclassified error became %s", got)
	}
}

func TestWrapPreservesTheChainForErrorsIs(t *testing.T) {
	sentinel := errors.New("driver failure")
	err := Wrap(sentinel, CodeConflict, "cannot update %s", "series")
	if !errors.Is(err, sentinel) {
		t.Fatal("the cause was dropped")
	}
	if Wrap(nil, CodeInternal, "ignored") != nil {
		t.Fatal("wrapping nil produced an error")
	}
	typed, ok := As(err)
	if !ok || typed.Code != CodeConflict {
		t.Fatalf("As returned %#v", typed)
	}
	if !strings.Contains(err.Error(), "driver failure") {
		t.Fatalf("the message dropped the cause: %s", err.Error())
	}
}

func TestDetailsAreOrderedInTheMessageAndCopiedOnRead(t *testing.T) {
	err := New(CodeInvalidArgument, "bad input").With("zeta", "3").With("alpha", "1")
	message := err.Error()
	if strings.Index(message, "alpha=1") > strings.Index(message, "zeta=3") {
		t.Fatalf("details are not sorted: %s", message)
	}
	details := err.DetailsCopy()
	details["alpha"] = "tampered"
	if err.Details["alpha"] != "1" {
		t.Fatal("DetailsCopy exposed the internal map")
	}
	empty := New(CodeInternal, "no details").DetailsCopy()
	if len(empty) != 0 {
		t.Fatalf("an error without details returned %#v", empty)
	}
}

func TestMessageFallsBackToASafeText(t *testing.T) {
	if Message(nil) != "" {
		t.Fatal("a nil error produced a message")
	}
	if got := Message(New(CodeNotFound, "series not found")); got != "series not found" {
		t.Fatalf("classified message is %q", got)
	}
	if got := Message(errors.New("driver stack trace")); got != "internal error" {
		t.Fatalf("an unclassified error leaked %q", got)
	}
	if got := Message(context.Canceled); !strings.Contains(got, "cancelled") {
		t.Fatalf("cancellation message is %q", got)
	}
	if got := Message(context.DeadlineExceeded); !strings.Contains(got, "deadline") {
		t.Fatalf("deadline message is %q", got)
	}
}

func TestHTTPStatusMapping(t *testing.T) {
	cases := map[Code]int{
		CodeInvalidArgument:    http.StatusBadRequest,
		CodeUnauthenticated:    http.StatusUnauthorized,
		CodePermissionDenied:   http.StatusForbidden,
		CodeNotFound:           http.StatusNotFound,
		CodeConflict:           http.StatusConflict,
		CodeFailedPrecondition: http.StatusUnprocessableEntity,
		CodeExhausted:          http.StatusTooManyRequests,
		CodeDeadlineExceeded:   http.StatusGatewayTimeout,
		CodeInternal:           http.StatusInternalServerError,
	}
	for code, want := range cases {
		if got := HTTPStatus(code); got != want {
			t.Fatalf("%s mapped to %d, want %d", code, got, want)
		}
	}
	if HTTPStatus(CodeCanceled) != 499 {
		t.Fatalf("cancellation mapped to %d", HTTPStatus(CodeCanceled))
	}
	if HTTPStatus(Code("unheard-of")) != http.StatusInternalServerError {
		t.Fatal("an unknown code did not fall back to 500")
	}
}

func TestFromContextOnlyReportsFinishedContexts(t *testing.T) {
	if err := FromContext(context.Background()); err != nil {
		t.Fatalf("a live context produced %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	err := FromContext(cancelled)
	if !IsCode(err, CodeCanceled) {
		t.Fatalf("cancelled context produced %v", CodeOf(err))
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatal("the context sentinel was dropped")
	}
}

func TestIsCodeMatchesThroughTheChain(t *testing.T) {
	err := Wrap(New(CodeNotFound, "shot not found"), CodeNotFound, "cannot load shot")
	if !IsCode(err, CodeNotFound) {
		t.Fatal("a wrapped not-found error was not recognised")
	}
	if IsCode(err, CodeConflict) {
		t.Fatal("the error matched an unrelated code")
	}
	if IsCode(nil, CodeInternal) {
		t.Fatal("a nil error matched a code")
	}
}
