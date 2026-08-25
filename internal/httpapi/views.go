package httpapi

import (
	"time"

	"github.com/vance1852/manjuflow-studio/internal/domain/identity"
	"github.com/vance1852/manjuflow-studio/internal/domain/production"
	"github.com/vance1852/manjuflow-studio/internal/domain/prompt"
	"github.com/vance1852/manjuflow-studio/internal/domain/render"
	"github.com/vance1852/manjuflow-studio/internal/domain/teaching"
)

// userView is the public projection of a studio member. The password digest is
// never part of any response.
type userView struct {
	ID          int64  `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	Status      string `json:"status"`
	CreatedAt   string `json:"created_at"`
}

func newUserView(user identity.User) userView {
	return userView{
		ID:          user.ID,
		Email:       user.Email,
		DisplayName: user.DisplayName,
		Role:        string(user.Role),
		Status:      string(user.Status),
		CreatedAt:   formatTime(user.CreatedAt),
	}
}

type templateView struct {
	ID          int64  `json:"id"`
	Slug        string `json:"slug"`
	Title       string `json:"title"`
	Discipline  string `json:"discipline"`
	HeadVersion int    `json:"head_version"`
	CreatedAt   string `json:"created_at"`
}

func newTemplateView(template prompt.Template) templateView {
	return templateView{
		ID:          template.ID,
		Slug:        template.Slug,
		Title:       template.Title,
		Discipline:  template.Discipline,
		HeadVersion: template.HeadVersion,
		CreatedAt:   formatTime(template.CreatedAt),
	}
}

type versionView struct {
	ID         int64  `json:"id"`
	TemplateID int64  `json:"template_id"`
	Version    int    `json:"version"`
	Status     string `json:"status"`
	Checksum   string `json:"checksum"`
	Notes      string `json:"notes"`
	CreatedAt  string `json:"created_at"`
	RetiredAt  string `json:"retired_at,omitempty"`
}

func newVersionView(version prompt.Version) versionView {
	view := versionView{
		ID:         version.ID,
		TemplateID: version.TemplateID,
		Version:    version.Version,
		Status:     string(version.Status),
		Checksum:   version.Checksum,
		Notes:      version.Notes,
		CreatedAt:  formatTime(version.CreatedAt),
	}
	if version.RetiredAt != nil {
		view.RetiredAt = formatTime(*version.RetiredAt)
	}
	return view
}

func newVersionViews(versions []prompt.Version) []versionView {
	out := make([]versionView, 0, len(versions))
	for _, version := range versions {
		out = append(out, newVersionView(version))
	}
	return out
}

type seriesView struct {
	ID          int64  `json:"id"`
	Code        string `json:"code"`
	Title       string `json:"title"`
	Logline     string `json:"logline"`
	State       string `json:"state"`
	Version     int    `json:"version"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	PublishedAt string `json:"published_at,omitempty"`
}

func newSeriesView(series production.Series) seriesView {
	view := seriesView{
		ID:        series.ID,
		Code:      series.Code,
		Title:     series.Title,
		Logline:   series.Logline,
		State:     string(series.State),
		Version:   series.Version,
		CreatedAt: formatTime(series.CreatedAt),
		UpdatedAt: formatTime(series.UpdatedAt),
	}
	if series.PublishedAt != nil {
		view.PublishedAt = formatTime(*series.PublishedAt)
	}
	return view
}

func newSeriesViews(items []production.Series) []seriesView {
	out := make([]seriesView, 0, len(items))
	for _, series := range items {
		out = append(out, newSeriesView(series))
	}
	return out
}

type shotView struct {
	ID              int64  `json:"id"`
	SeriesID        int64  `json:"series_id"`
	Ordinal         int    `json:"ordinal"`
	Title           string `json:"title"`
	Direction       string `json:"direction"`
	State           string `json:"state"`
	PromptVersionID int64  `json:"prompt_version_id,omitempty"`
	ArtifactRef     string `json:"artifact_ref,omitempty"`
	ReworkReason    string `json:"rework_reason,omitempty"`
	Version         int    `json:"version"`
	UpdatedAt       string `json:"updated_at"`
}

func newShotView(shot production.Shot) shotView {
	view := shotView{
		ID:           shot.ID,
		SeriesID:     shot.SeriesID,
		Ordinal:      shot.Ordinal,
		Title:        shot.Title,
		Direction:    shot.Direction,
		State:        string(shot.State),
		ArtifactRef:  shot.ArtifactRef,
		ReworkReason: shot.ReworkReason,
		Version:      shot.Version,
		UpdatedAt:    formatTime(shot.UpdatedAt),
	}
	if shot.PromptVersionID != nil {
		view.PromptVersionID = *shot.PromptVersionID
	}
	return view
}

func newShotViews(shots []production.Shot) []shotView {
	out := make([]shotView, 0, len(shots))
	for _, shot := range shots {
		out = append(out, newShotView(shot))
	}
	return out
}

type jobView struct {
	ID            int64  `json:"id"`
	ShotID        int64  `json:"shot_id"`
	State         string `json:"state"`
	Attempts      int    `json:"attempts"`
	MaxAttempts   int    `json:"max_attempts"`
	NextAttemptAt string `json:"next_attempt_at"`
	ArtifactRef   string `json:"artifact_ref,omitempty"`
	LastError     string `json:"last_error,omitempty"`
	QuotaDay      string `json:"quota_day"`
}

func newJobView(job render.Job) jobView {
	return jobView{
		ID:            job.ID,
		ShotID:        job.ShotID,
		State:         string(job.State),
		Attempts:      job.Attempts,
		MaxAttempts:   job.MaxAttempts,
		NextAttemptAt: formatTime(job.NextAttemptAt),
		ArtifactRef:   job.ArtifactRef,
		LastError:     job.LastError,
		QuotaDay:      job.QuotaDay,
	}
}

func newJobViews(jobs []render.Job) []jobView {
	out := make([]jobView, 0, len(jobs))
	for _, job := range jobs {
		out = append(out, newJobView(job))
	}
	return out
}

type workshopView struct {
	ID              int64  `json:"id"`
	SeriesID        int64  `json:"series_id"`
	ShotID          int64  `json:"shot_id"`
	PromptVersionID int64  `json:"prompt_version_id"`
	Title           string `json:"title"`
	Brief           string `json:"brief"`
	State           string `json:"state"`
	Capacity        int    `json:"capacity"`
	Enrolled        int    `json:"enrolled"`
	OpensAt         string `json:"opens_at"`
	ClosesAt        string `json:"closes_at"`
	Version         int    `json:"version"`
}

func newWorkshopView(workshop teaching.Workshop) workshopView {
	return workshopView{
		ID:              workshop.ID,
		SeriesID:        workshop.SeriesID,
		ShotID:          workshop.ShotID,
		PromptVersionID: workshop.PromptVersionID,
		Title:           workshop.Title,
		Brief:           workshop.Brief,
		State:           string(workshop.State),
		Capacity:        workshop.Capacity,
		Enrolled:        workshop.Enrolled,
		OpensAt:         formatTime(workshop.OpensAt),
		ClosesAt:        formatTime(workshop.ClosesAt),
		Version:         workshop.Version,
	}
}

func newWorkshopViews(items []teaching.Workshop) []workshopView {
	out := make([]workshopView, 0, len(items))
	for _, workshop := range items {
		out = append(out, newWorkshopView(workshop))
	}
	return out
}

type submissionView struct {
	ID           int64  `json:"id"`
	WorkshopID   int64  `json:"workshop_id"`
	EnrollmentID int64  `json:"enrollment_id"`
	State        string `json:"state"`
	Score        int    `json:"score"`
	Feedback     string `json:"feedback,omitempty"`
	CreatedAt    string `json:"created_at"`
	ReviewedAt   string `json:"reviewed_at,omitempty"`
}

func newSubmissionView(submission teaching.Submission) submissionView {
	view := submissionView{
		ID:           submission.ID,
		WorkshopID:   submission.WorkshopID,
		EnrollmentID: submission.EnrollmentID,
		State:        string(submission.State),
		Score:        submission.Score,
		Feedback:     submission.Feedback,
		CreatedAt:    formatTime(submission.CreatedAt),
	}
	if submission.ReviewedAt != nil {
		view.ReviewedAt = formatTime(*submission.ReviewedAt)
	}
	return view
}

func newSubmissionViews(items []teaching.Submission) []submissionView {
	out := make([]submissionView, 0, len(items))
	for _, submission := range items {
		out = append(out, newSubmissionView(submission))
	}
	return out
}

type enrollmentView struct {
	ID         int64  `json:"id"`
	WorkshopID int64  `json:"workshop_id"`
	State      string `json:"state"`
	CreatedAt  string `json:"created_at"`
}

func newEnrollmentView(enrollment teaching.Enrollment) enrollmentView {
	return enrollmentView{
		ID:         enrollment.ID,
		WorkshopID: enrollment.WorkshopID,
		State:      string(enrollment.State),
		CreatedAt:  formatTime(enrollment.CreatedAt),
	}
}

func formatTime(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.Format(time.RFC3339)
}
