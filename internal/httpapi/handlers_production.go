package httpapi

import (
	"net/http"
	"strings"

	"github.com/vance1852/manjuflow-studio/internal/apperr"
	"github.com/vance1852/manjuflow-studio/internal/domain/production"
	"github.com/vance1852/manjuflow-studio/internal/idempotency"
	"github.com/vance1852/manjuflow-studio/internal/repository"
	"github.com/vance1852/manjuflow-studio/internal/service/productionsvc"
	"github.com/vance1852/manjuflow-studio/internal/service/promptsvc"
)

type createTemplateRequest struct {
	Slug       string `json:"slug"`
	Title      string `json:"title"`
	Discipline string `json:"discipline"`
}

func (s *Server) handleCreateTemplate(w http.ResponseWriter, r *http.Request) {
	var payload createTemplateRequest
	if err := decodeJSON(r, &payload); err != nil {
		WriteError(w, r, err)
		return
	}
	template, err := s.prompts.CreateTemplate(r.Context(), promptsvc.CreateTemplateInput{
		Slug:       payload.Slug,
		Title:      payload.Title,
		Discipline: payload.Discipline,
	})
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, newTemplateView(template))
}

type appendVersionRequest struct {
	Body     string `json:"body"`
	Notes    string `json:"notes"`
	Activate bool   `json:"activate"`
}

func (s *Server) handleAppendVersion(w http.ResponseWriter, r *http.Request) {
	templateID, err := idParam(r, "templateID")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	var payload appendVersionRequest
	if err := decodeJSON(r, &payload); err != nil {
		WriteError(w, r, err)
		return
	}
	version, err := s.prompts.AppendVersion(r.Context(), promptsvc.AppendVersionInput{
		TemplateID: templateID,
		Body:       payload.Body,
		Notes:      payload.Notes,
		Activate:   payload.Activate,
	})
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, newVersionView(version))
}

func (s *Server) handleListVersions(w http.ResponseWriter, r *http.Request) {
	templateID, err := idParam(r, "templateID")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	page, err := parsePage(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	versions, total, err := s.prompts.ListVersions(r.Context(), templateID, page)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, pageEnvelope{
		Items:  newVersionViews(versions),
		Total:  total,
		Limit:  effectiveLimit(page.Limit),
		Offset: page.Offset,
	})
}

func (s *Server) handleActivateVersion(w http.ResponseWriter, r *http.Request) {
	versionID, err := idParam(r, "versionID")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	version, err := s.prompts.ActivateVersion(r.Context(), versionID)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newVersionView(version))
}

func (s *Server) handleRetireVersion(w http.ResponseWriter, r *http.Request) {
	versionID, err := idParam(r, "versionID")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	version, err := s.prompts.RetireVersion(r.Context(), versionID)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newVersionView(version))
}

type referencesResponse struct {
	Shots     int `json:"bound_shots"`
	Jobs      int `json:"unfinished_jobs"`
	Workshops int `json:"live_workshops"`
	Total     int `json:"total"`
}

func (s *Server) handleVersionReferences(w http.ResponseWriter, r *http.Request) {
	versionID, err := idParam(r, "versionID")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	references, err := s.prompts.CountReferences(r.Context(), versionID)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, referencesResponse{
		Shots:     references.Shots,
		Jobs:      references.Jobs,
		Workshops: references.Workshops,
		Total:     references.Total(),
	})
}

type createSeriesRequest struct {
	Title      string `json:"title"`
	Logline    string `json:"logline"`
	CodePrefix string `json:"code_prefix"`
}

func (s *Server) handleCreateSeries(w http.ResponseWriter, r *http.Request) {
	var payload createSeriesRequest
	if err := decodeJSON(r, &payload); err != nil {
		WriteError(w, r, err)
		return
	}
	series, err := s.production.CreateSeries(r.Context(), productionsvc.CreateSeriesInput{
		Title:      payload.Title,
		Logline:    payload.Logline,
		CodePrefix: payload.CodePrefix,
	})
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, newSeriesView(series))
}

func (s *Server) handleListSeries(w http.ResponseWriter, r *http.Request) {
	page, err := parsePage(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	filter := repository.SeriesFilter{TitleLike: strings.TrimSpace(r.URL.Query().Get("title"))}
	for _, raw := range r.URL.Query()["state"] {
		for _, candidate := range strings.Split(raw, ",") {
			if strings.TrimSpace(candidate) == "" {
				continue
			}
			state, parseErr := production.ParseSeriesState(candidate)
			if parseErr != nil {
				WriteError(w, r, parseErr)
				return
			}
			filter.States = append(filter.States, state)
		}
	}
	items, total, err := s.production.ListSeries(r.Context(), filter, page)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, pageEnvelope{
		Items:  newSeriesViews(items),
		Total:  total,
		Limit:  effectiveLimit(page.Limit),
		Offset: page.Offset,
	})
}

type seriesDetailResponse struct {
	Series seriesView     `json:"series"`
	Shots  []shotView     `json:"shots"`
	Counts map[string]int `json:"shot_counts"`
}

func (s *Server) handleGetSeries(w http.ResponseWriter, r *http.Request) {
	seriesID, err := idParam(r, "seriesID")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	detail, err := s.production.GetSeries(r.Context(), seriesID)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	counts := make(map[string]int, len(detail.Counts))
	for state, total := range detail.Counts {
		counts[string(state)] = total
	}
	writeJSON(w, http.StatusOK, seriesDetailResponse{
		Series: newSeriesView(detail.Series),
		Shots:  newShotViews(detail.Shots),
		Counts: counts,
	})
}

type planShotsRequest struct {
	Shots []struct {
		Ordinal   int    `json:"ordinal"`
		Title     string `json:"title"`
		Direction string `json:"direction"`
	} `json:"shots"`
}

type planShotsResponse struct {
	Results []planResultView `json:"results"`
	Created int              `json:"created"`
}

type planResultView struct {
	Ordinal int    `json:"ordinal"`
	ShotID  int64  `json:"shot_id,omitempty"`
	Created bool   `json:"created"`
	Error   string `json:"error,omitempty"`
}

func (s *Server) handlePlanShots(w http.ResponseWriter, r *http.Request) {
	seriesID, err := idParam(r, "seriesID")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	var payload planShotsRequest
	if err := decodeJSON(r, &payload); err != nil {
		WriteError(w, r, err)
		return
	}
	drafts := make([]production.Draft, 0, len(payload.Shots))
	for _, item := range payload.Shots {
		drafts = append(drafts, production.Draft{Ordinal: item.Ordinal, Title: item.Title, Direction: item.Direction})
	}
	results, err := s.production.PlanShots(r.Context(), seriesID, drafts)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	response := planShotsResponse{Results: make([]planResultView, 0, len(results))}
	for _, result := range results {
		if result.Created {
			response.Created++
		}
		response.Results = append(response.Results, planResultView{
			Ordinal: result.Ordinal,
			ShotID:  result.ShotID,
			Created: result.Created,
			Error:   result.Error,
		})
	}
	status := http.StatusCreated
	if response.Created < len(results) {
		status = http.StatusMultiStatus
	}
	writeJSON(w, status, response)
}

type bindPromptRequest struct {
	PromptVersionID int64 `json:"prompt_version_id"`
}

func (s *Server) handleBindPrompt(w http.ResponseWriter, r *http.Request) {
	shotID, err := idParam(r, "shotID")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	var payload bindPromptRequest
	if err := decodeJSON(r, &payload); err != nil {
		WriteError(w, r, err)
		return
	}
	if payload.PromptVersionID <= 0 {
		WriteError(w, r, apperr.New(apperr.CodeInvalidArgument, "prompt_version_id must be a positive integer").
			With("field", "prompt_version_id"))
		return
	}
	shot, err := s.production.BindPrompt(r.Context(), shotID, payload.PromptVersionID)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newShotView(shot))
}

func (s *Server) handleSubmitRender(w http.ResponseWriter, r *http.Request) {
	shotID, err := idParam(r, "shotID")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	key, err := idempotency.ValidateKey(r.Header.Get("Idempotency-Key"))
	if err != nil {
		WriteError(w, r, err)
		return
	}
	result, err := s.production.SubmitRender(r.Context(), shotID, key)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	if result.Replayed {
		w.Header().Set("Idempotent-Replay", "true")
	}
	writeJSON(w, http.StatusAccepted, result)
}

type reviewShotRequest struct {
	Approve bool   `json:"approve"`
	Reason  string `json:"reason"`
}

func (s *Server) handleReviewShot(w http.ResponseWriter, r *http.Request) {
	shotID, err := idParam(r, "shotID")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	var payload reviewShotRequest
	if err := decodeJSON(r, &payload); err != nil {
		WriteError(w, r, err)
		return
	}
	shot, err := s.production.ReviewShot(r.Context(), productionsvc.ReviewShotInput{
		ShotID:  shotID,
		Approve: payload.Approve,
		Reason:  payload.Reason,
	})
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newShotView(shot))
}

func (s *Server) handlePublishSeries(w http.ResponseWriter, r *http.Request) {
	seriesID, err := idParam(r, "seriesID")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	series, err := s.production.PublishSeries(r.Context(), seriesID)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newSeriesView(series))
}

func (s *Server) handleListRenderJobs(w http.ResponseWriter, r *http.Request) {
	shotID, err := idParam(r, "shotID")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	jobs, err := s.production.ListRenderJobs(r.Context(), shotID)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, pageEnvelope{Items: newJobViews(jobs), Total: len(jobs), Limit: len(jobs)})
}

type quotaResponse struct {
	Day       string `json:"quota_day"`
	Capacity  int    `json:"capacity"`
	Used      int    `json:"used"`
	Remaining int    `json:"remaining"`
}

func (s *Server) handleQuota(w http.ResponseWriter, r *http.Request) {
	quota, err := s.production.RemainingQuota(r.Context())
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, quotaResponse{
		Day:       quota.Day,
		Capacity:  quota.Capacity,
		Used:      quota.Used,
		Remaining: quota.Remaining(),
	})
}

func effectiveLimit(limit int) int {
	if limit <= 0 {
		return repository.DefaultLimit
	}
	return limit
}
