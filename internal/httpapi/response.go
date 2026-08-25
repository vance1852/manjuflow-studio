// Package httpapi exposes the studio HTTP contract: JSON only, one error
// envelope, explicit request identifiers and no SQL anywhere near a handler.
package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/vance1852/manjuflow-studio/internal/apperr"
	"github.com/vance1852/manjuflow-studio/internal/repository"
	"github.com/vance1852/manjuflow-studio/internal/reqctx"
)

// maxBodyBytes bounds a request payload.
const maxBodyBytes = 1 << 20

// errorEnvelope is the single error shape of the API.
type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code      string            `json:"code"`
	Message   string            `json:"message"`
	RequestID string            `json:"request_id"`
	Details   map[string]string `json:"details,omitempty"`
}

// pageEnvelope wraps a list response with its pagination metadata.
type pageEnvelope struct {
	Items  any `json:"items"`
	Total  int `json:"total"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if payload == nil {
		return
	}
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(payload)
}

// WriteError renders any error as the unified envelope with the mapped status.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	code := apperr.CodeOf(err)
	body := errorBody{
		Code:      string(code),
		Message:   apperr.Message(err),
		RequestID: reqctx.RequestID(r.Context()),
	}
	if typed, ok := apperr.As(err); ok {
		if details := typed.DetailsCopy(); len(details) > 0 {
			body.Details = details
		}
	}
	writeJSON(w, apperr.HTTPStatus(code), errorEnvelope{Error: body})
}

func decodeJSON(r *http.Request, target any) error {
	if r.Body == nil {
		return apperr.New(apperr.CodeInvalidArgument, "request body is required")
	}
	limited := http.MaxBytesReader(nil, r.Body, maxBodyBytes)
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		if errors.Is(err, io.EOF) {
			return apperr.New(apperr.CodeInvalidArgument, "request body is required")
		}
		return apperr.Wrap(err, apperr.CodeInvalidArgument, "request body is not valid JSON for this endpoint")
	}
	return nil
}

// idParam reads a positive integer path parameter.
func idParam(r *http.Request, name string) (int64, error) {
	raw := strings.TrimSpace(r.PathValue(name))
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, apperr.New(apperr.CodeInvalidArgument, "%s must be a positive integer", name).
			With("field", name)
	}
	return id, nil
}

func parsePage(r *http.Request) (repository.Page, error) {
	query := r.URL.Query()
	page := repository.Page{SortBy: strings.TrimSpace(query.Get("sort_by"))}
	if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil {
			return repository.Page{}, apperr.Wrap(err, apperr.CodeInvalidArgument, "limit must be an integer").
				With("field", "limit")
		}
		page.Limit = limit
	}
	if raw := strings.TrimSpace(query.Get("offset")); raw != "" {
		offset, err := strconv.Atoi(raw)
		if err != nil {
			return repository.Page{}, apperr.Wrap(err, apperr.CodeInvalidArgument, "offset must be an integer").
				With("field", "offset")
		}
		page.Offset = offset
	}
	switch strings.ToLower(strings.TrimSpace(query.Get("order"))) {
	case "", "asc":
	case "desc":
		page.Desc = true
	default:
		return repository.Page{}, apperr.New(apperr.CodeInvalidArgument, "order must be asc or desc").
			With("field", "order")
	}
	return page, nil
}

// notFound renders the fallback route error.
func notFound(w http.ResponseWriter, r *http.Request) {
	WriteError(w, r, apperr.New(apperr.CodeNotFound, "route %s %s is unknown", r.Method, r.URL.Path))
}
