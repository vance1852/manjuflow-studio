package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/vance1852/manjuflow-studio/internal/apperr"
	"github.com/vance1852/manjuflow-studio/internal/domain/teaching"
	"github.com/vance1852/manjuflow-studio/internal/repository"
	"github.com/vance1852/manjuflow-studio/internal/service/teachingsvc"
)

type openWorkshopRequest struct {
	ShotID   int64  `json:"shot_id"`
	Title    string `json:"title"`
	Brief    string `json:"brief"`
	Capacity int    `json:"capacity"`
	OpensAt  string `json:"opens_at"`
	ClosesAt string `json:"closes_at"`
}

func (s *Server) handleOpenWorkshop(w http.ResponseWriter, r *http.Request) {
	var payload openWorkshopRequest
	if err := decodeJSON(r, &payload); err != nil {
		WriteError(w, r, err)
		return
	}
	opensAt, err := parseSchedule(payload.OpensAt, "opens_at", s.now())
	if err != nil {
		WriteError(w, r, err)
		return
	}
	closesAt, err := parseSchedule(payload.ClosesAt, "closes_at", time.Time{})
	if err != nil {
		WriteError(w, r, err)
		return
	}
	workshop, err := s.teaching.OpenWorkshop(r.Context(), teachingsvc.OpenWorkshopInput{
		ShotID:   payload.ShotID,
		Title:    payload.Title,
		Brief:    payload.Brief,
		Capacity: payload.Capacity,
		OpensAt:  opensAt,
		ClosesAt: closesAt,
	})
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, newWorkshopView(workshop))
}

func (s *Server) handleListWorkshops(w http.ResponseWriter, r *http.Request) {
	page, err := parsePage(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	filter := repository.WorkshopFilter{}
	for _, raw := range r.URL.Query()["state"] {
		for _, candidate := range strings.Split(raw, ",") {
			if strings.TrimSpace(candidate) == "" {
				continue
			}
			state, parseErr := teaching.ParseWorkshopState(candidate)
			if parseErr != nil {
				WriteError(w, r, parseErr)
				return
			}
			filter.States = append(filter.States, state)
		}
	}
	items, total, err := s.teaching.ListWorkshops(r.Context(), filter, page)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, pageEnvelope{
		Items:  newWorkshopViews(items),
		Total:  total,
		Limit:  effectiveLimit(page.Limit),
		Offset: page.Offset,
	})
}

func (s *Server) handleEnroll(w http.ResponseWriter, r *http.Request) {
	workshopID, err := idParam(r, "workshopID")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	enrollment, err := s.teaching.Enroll(r.Context(), workshopID)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, newEnrollmentView(enrollment))
}

type submitPracticeRequest struct {
	Body string `json:"body"`
}

func (s *Server) handleSubmitPractice(w http.ResponseWriter, r *http.Request) {
	workshopID, err := idParam(r, "workshopID")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	var payload submitPracticeRequest
	if err := decodeJSON(r, &payload); err != nil {
		WriteError(w, r, err)
		return
	}
	submission, err := s.teaching.SubmitPractice(r.Context(), teachingsvc.SubmitPracticeInput{
		WorkshopID: workshopID,
		Body:       payload.Body,
	})
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, newSubmissionView(submission))
}

func (s *Server) handleListSubmissions(w http.ResponseWriter, r *http.Request) {
	workshopID, err := idParam(r, "workshopID")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	page, err := parsePage(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	items, total, err := s.teaching.ListSubmissions(r.Context(), workshopID, page)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, pageEnvelope{
		Items:  newSubmissionViews(items),
		Total:  total,
		Limit:  effectiveLimit(page.Limit),
		Offset: page.Offset,
	})
}

type reviewPracticeRequest struct {
	Score    int    `json:"score"`
	Feedback string `json:"feedback"`
}

func (s *Server) handleReviewPractice(w http.ResponseWriter, r *http.Request) {
	submissionID, err := idParam(r, "submissionID")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	var payload reviewPracticeRequest
	if err := decodeJSON(r, &payload); err != nil {
		WriteError(w, r, err)
		return
	}
	submission, err := s.teaching.ReviewPractice(r.Context(), teachingsvc.ReviewPracticeInput{
		SubmissionID: submissionID,
		Score:        payload.Score,
		Feedback:     payload.Feedback,
	})
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newSubmissionView(submission))
}

func (s *Server) handleStartGrading(w http.ResponseWriter, r *http.Request) {
	workshopID, err := idParam(r, "workshopID")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	workshop, err := s.teaching.StartGrading(r.Context(), workshopID)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newWorkshopView(workshop))
}

func (s *Server) handleCloseWorkshop(w http.ResponseWriter, r *http.Request) {
	workshopID, err := idParam(r, "workshopID")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	workshop, err := s.teaching.CloseWorkshop(r.Context(), workshopID)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newWorkshopView(workshop))
}

func parseSchedule(raw, field string, fallback time.Time) (time.Time, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		if fallback.IsZero() {
			return time.Time{}, apperr.New(apperr.CodeInvalidArgument, "%s is required", field).With("field", field)
		}
		return fallback, nil
	}
	parsed, err := time.Parse(time.RFC3339, trimmed)
	if err != nil {
		return time.Time{}, apperr.Wrap(err, apperr.CodeInvalidArgument, "%s must be an RFC3339 timestamp", field).
			With("field", field)
	}
	return parsed, nil
}
