package sqliterepo

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/vance1852/manjuflow-studio/internal/domain/teaching"
	"github.com/vance1852/manjuflow-studio/internal/repository"
)

// TeachingStore implements repository.TeachingRepository.
type TeachingStore struct{}

// NewTeachingStore builds the teaching repository.
func NewTeachingStore() TeachingStore { return TeachingStore{} }

const workshopColumns = `id, studio_id, series_id, shot_id, prompt_version_id, mentor_id, title, brief, state,
	capacity, enrolled, opens_at, closes_at, version, created_at, updated_at`

// CreateWorkshop inserts a teaching workshop.
func (TeachingStore) CreateWorkshop(ctx context.Context, q repository.Querier, workshop teaching.Workshop) (int64, error) {
	return insert(ctx, q, "cannot create workshop",
		`INSERT INTO workshops (studio_id, series_id, shot_id, prompt_version_id, mentor_id, title, brief, state,
		 capacity, enrolled, opens_at, closes_at, version, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		workshop.StudioID, workshop.SeriesID, workshop.ShotID, workshop.PromptVersionID, workshop.MentorID,
		workshop.Title, workshop.Brief, string(workshop.State), workshop.Capacity, workshop.Enrolled,
		encodeTime(workshop.OpensAt), encodeTime(workshop.ClosesAt), workshop.Version,
		encodeTime(workshop.CreatedAt), encodeTime(workshop.UpdatedAt))
}

// FindWorkshopByID loads a workshop by identifier.
func (TeachingStore) FindWorkshopByID(ctx context.Context, q repository.Querier, id int64) (teaching.Workshop, error) {
	row := q.QueryRowContext(ctx, `SELECT `+workshopColumns+` FROM workshops WHERE id = ?`, id)
	return scanWorkshop(row, id)
}

// SaveWorkshop persists a workshop with optimistic locking. The seat counter is
// owned by ReserveSeat and is never overwritten here.
func (TeachingStore) SaveWorkshop(ctx context.Context, q repository.Querier, workshop teaching.Workshop) error {
	affected, err := execExpectingRow(ctx, q, "cannot update workshop",
		`UPDATE workshops SET title = ?, brief = ?, state = ?, capacity = ?, opens_at = ?, closes_at = ?,
		 version = version + 1, updated_at = ?
		 WHERE id = ? AND version = ?`,
		workshop.Title, workshop.Brief, string(workshop.State), workshop.Capacity,
		encodeTime(workshop.OpensAt), encodeTime(workshop.ClosesAt), encodeTime(workshop.UpdatedAt),
		workshop.ID, workshop.Version)
	if err != nil {
		return err
	}
	if affected == 0 {
		return staleWrite("workshop", workshop.ID, workshop.Version)
	}
	return nil
}

// ReserveSeat takes one seat with a conditional update so a full workshop can
// never accept an extra apprentice under concurrency.
func (TeachingStore) ReserveSeat(ctx context.Context, q repository.Querier, workshopID int64, now time.Time) (bool, error) {
	affected, err := execExpectingRow(ctx, q, "cannot reserve workshop seat",
		`UPDATE workshops SET enrolled = enrolled + 1, updated_at = ?
		 WHERE id = ? AND state = 'open' AND enrolled < capacity AND opens_at <= ? AND closes_at > ?`,
		encodeTime(now), workshopID, encodeTime(now), encodeTime(now))
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

var workshopSortColumns = map[string]string{
	"created_at": "created_at",
	"closes_at":  "closes_at",
	"title":      "title",
	"state":      "state",
}

// WorkshopSortColumns exposes the allowed sort keys for workshop listings.
func WorkshopSortColumns() map[string]string { return copyColumns(workshopSortColumns) }

// ListWorkshops returns one page of workshops plus the matching total.
func (TeachingStore) ListWorkshops(ctx context.Context, q repository.Querier, studioID int64, filter repository.WorkshopFilter, page repository.Page) ([]teaching.Workshop, int, error) {
	normalised, err := repository.NormalisePage(page, workshopSortColumns, "closes_at")
	if err != nil {
		return nil, 0, err
	}
	where := []string{"studio_id = ?"}
	args := []any{studioID}
	if len(filter.States) > 0 {
		placeholders := make([]string, 0, len(filter.States))
		for _, state := range filter.States {
			placeholders = append(placeholders, "?")
			args = append(args, string(state))
		}
		where = append(where, "state IN ("+strings.Join(placeholders, ", ")+")")
	}
	if filter.SeriesID > 0 {
		where = append(where, "series_id = ?")
		args = append(args, filter.SeriesID)
	}
	if filter.MentorID > 0 {
		where = append(where, "mentor_id = ?")
		args = append(args, filter.MentorID)
	}
	clause := " WHERE " + strings.Join(where, " AND ")

	total, err := countRows(ctx, q, "cannot count workshops", `SELECT COUNT(*) FROM workshops`+clause, args...)
	if err != nil {
		return nil, 0, err
	}
	statement := `SELECT ` + workshopColumns + ` FROM workshops` + clause +
		orderClause(repository.Column(workshopSortColumns, normalised.SortBy), normalised.Desc) +
		` LIMIT ? OFFSET ?`
	pageArgs := append(append([]any{}, args...), normalised.Limit, normalised.Offset)
	rows, err := q.QueryContext(ctx, statement, pageArgs...)
	if err != nil {
		return nil, 0, wrap(err, "cannot list workshops")
	}
	defer func() { _ = rows.Close() }()

	out := make([]teaching.Workshop, 0, normalised.Limit)
	for rows.Next() {
		workshop, err := scanWorkshopRows(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, workshop)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, wrap(err, "cannot iterate workshops")
	}
	return out, total, nil
}

// CountLiveWorkshopsForPromptVersion counts workshops still holding a version.
func (TeachingStore) CountLiveWorkshopsForPromptVersion(ctx context.Context, q repository.Querier, promptVersionID int64) (int, error) {
	return countRows(ctx, q, "cannot count workshops",
		`SELECT COUNT(*) FROM workshops WHERE prompt_version_id = ? AND state IN ('open', 'teaching', 'grading')`,
		promptVersionID)
}

// CountLiveWorkshopsForShot counts workshops still teaching a shot.
func (TeachingStore) CountLiveWorkshopsForShot(ctx context.Context, q repository.Querier, shotID int64) (int, error) {
	return countRows(ctx, q, "cannot count workshops",
		`SELECT COUNT(*) FROM workshops WHERE shot_id = ? AND state IN ('open', 'teaching', 'grading')`, shotID)
}

// ListOverdueWorkshops returns teaching workshops whose deadline already passed.
func (TeachingStore) ListOverdueWorkshops(ctx context.Context, q repository.Querier, now time.Time, limit int) ([]teaching.Workshop, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT `+workshopColumns+` FROM workshops
		 WHERE state = 'teaching' AND closes_at <= ?
		 ORDER BY closes_at ASC, id ASC LIMIT ?`, encodeTime(now), limit)
	if err != nil {
		return nil, wrap(err, "cannot list overdue workshops")
	}
	defer func() { _ = rows.Close() }()

	var out []teaching.Workshop
	for rows.Next() {
		workshop, err := scanWorkshopRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, workshop)
	}
	if err := rows.Err(); err != nil {
		return nil, wrap(err, "cannot iterate overdue workshops")
	}
	return out, nil
}

// CreateEnrollment inserts an apprentice seat.
func (TeachingStore) CreateEnrollment(ctx context.Context, q repository.Querier, enrollment teaching.Enrollment) (int64, error) {
	return insert(ctx, q, "cannot create enrollment",
		`INSERT INTO enrollments (workshop_id, apprentice_id, state, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		enrollment.WorkshopID, enrollment.ApprenticeID, string(enrollment.State),
		encodeTime(enrollment.CreatedAt), encodeTime(enrollment.UpdatedAt))
}

// FindEnrollment loads one apprentice seat.
func (TeachingStore) FindEnrollment(ctx context.Context, q repository.Querier, workshopID, apprenticeID int64) (teaching.Enrollment, error) {
	var (
		enrollment teaching.Enrollment
		state      string
		createdAt  string
		updatedAt  string
	)
	err := q.QueryRowContext(ctx,
		`SELECT id, workshop_id, apprentice_id, state, created_at, updated_at
		 FROM enrollments WHERE workshop_id = ? AND apprentice_id = ?`, workshopID, apprenticeID).
		Scan(&enrollment.ID, &enrollment.WorkshopID, &enrollment.ApprenticeID, &state, &createdAt, &updatedAt)
	if err != nil {
		if noRows(err) {
			return teaching.Enrollment{}, repository.NotFound("enrollment", apprenticeID)
		}
		return teaching.Enrollment{}, wrap(err, "cannot read enrollment")
	}
	if enrollment.CreatedAt, err = decodeTime(createdAt); err != nil {
		return teaching.Enrollment{}, err
	}
	if enrollment.UpdatedAt, err = decodeTime(updatedAt); err != nil {
		return teaching.Enrollment{}, err
	}
	enrollment.State = teaching.EnrollmentState(state)
	return enrollment, nil
}

// FindEnrollmentByID loads one apprentice seat by identifier.
func (TeachingStore) FindEnrollmentByID(ctx context.Context, q repository.Querier, id int64) (teaching.Enrollment, error) {
	var (
		enrollment teaching.Enrollment
		state      string
		createdAt  string
		updatedAt  string
	)
	err := q.QueryRowContext(ctx,
		`SELECT id, workshop_id, apprentice_id, state, created_at, updated_at FROM enrollments WHERE id = ?`, id).
		Scan(&enrollment.ID, &enrollment.WorkshopID, &enrollment.ApprenticeID, &state, &createdAt, &updatedAt)
	if err != nil {
		if noRows(err) {
			return teaching.Enrollment{}, repository.NotFound("enrollment", id)
		}
		return teaching.Enrollment{}, wrap(err, "cannot read enrollment")
	}
	if enrollment.CreatedAt, err = decodeTime(createdAt); err != nil {
		return teaching.Enrollment{}, err
	}
	if enrollment.UpdatedAt, err = decodeTime(updatedAt); err != nil {
		return teaching.Enrollment{}, err
	}
	enrollment.State = teaching.EnrollmentState(state)
	return enrollment, nil
}

// SaveEnrollment persists an enrolment state change.
func (TeachingStore) SaveEnrollment(ctx context.Context, q repository.Querier, enrollment teaching.Enrollment) error {
	affected, err := execExpectingRow(ctx, q, "cannot update enrollment",
		`UPDATE enrollments SET state = ?, updated_at = ? WHERE id = ?`,
		string(enrollment.State), encodeTime(enrollment.UpdatedAt), enrollment.ID)
	if err != nil {
		return err
	}
	if affected == 0 {
		return repository.NotFound("enrollment", enrollment.ID)
	}
	return nil
}

const submissionColumns = `id, workshop_id, enrollment_id, prompt_version_id, body, state, score, feedback,
	reviewed_by, reviewed_at, version, created_at, updated_at`

// CreateSubmission inserts a practice submission.
func (TeachingStore) CreateSubmission(ctx context.Context, q repository.Querier, submission teaching.Submission) (int64, error) {
	return insert(ctx, q, "cannot create practice submission",
		`INSERT INTO practice_submissions (workshop_id, enrollment_id, prompt_version_id, body, state, score,
		 feedback, reviewed_by, reviewed_at, version, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		submission.WorkshopID, submission.EnrollmentID, submission.PromptVersionID, submission.Body,
		string(submission.State), submission.Score, submission.Feedback, nullableInt64(submission.ReviewedBy),
		encodeNullableTime(submission.ReviewedAt), submission.Version,
		encodeTime(submission.CreatedAt), encodeTime(submission.UpdatedAt))
}

// FindSubmissionByID loads a submission by identifier.
func (TeachingStore) FindSubmissionByID(ctx context.Context, q repository.Querier, id int64) (teaching.Submission, error) {
	row := q.QueryRowContext(ctx, `SELECT `+submissionColumns+` FROM practice_submissions WHERE id = ?`, id)
	return scanSubmission(row, id)
}

// SaveSubmission persists a review outcome with optimistic locking.
func (TeachingStore) SaveSubmission(ctx context.Context, q repository.Querier, submission teaching.Submission) error {
	affected, err := execExpectingRow(ctx, q, "cannot update practice submission",
		`UPDATE practice_submissions SET state = ?, score = ?, feedback = ?, reviewed_by = ?, reviewed_at = ?,
		 version = version + 1, updated_at = ?
		 WHERE id = ? AND version = ?`,
		string(submission.State), submission.Score, submission.Feedback, nullableInt64(submission.ReviewedBy),
		encodeNullableTime(submission.ReviewedAt), encodeTime(submission.UpdatedAt),
		submission.ID, submission.Version)
	if err != nil {
		return err
	}
	if affected == 0 {
		return staleWrite("practice submission", submission.ID, submission.Version)
	}
	return nil
}

var submissionSortColumns = map[string]string{
	"created_at": "created_at",
	"score":      "score",
	"state":      "state",
}

// SubmissionSortColumns exposes the allowed sort keys for submission listings.
func SubmissionSortColumns() map[string]string { return copyColumns(submissionSortColumns) }

// ListSubmissions returns one page of submissions plus the matching total.
func (TeachingStore) ListSubmissions(ctx context.Context, q repository.Querier, workshopID int64, page repository.Page) ([]teaching.Submission, int, error) {
	normalised, err := repository.NormalisePage(page, submissionSortColumns, "created_at")
	if err != nil {
		return nil, 0, err
	}
	total, err := countRows(ctx, q, "cannot count practice submissions",
		`SELECT COUNT(*) FROM practice_submissions WHERE workshop_id = ?`, workshopID)
	if err != nil {
		return nil, 0, err
	}
	statement := `SELECT ` + submissionColumns + ` FROM practice_submissions WHERE workshop_id = ?` +
		orderClause(repository.Column(submissionSortColumns, normalised.SortBy), normalised.Desc) +
		` LIMIT ? OFFSET ?`
	rows, err := q.QueryContext(ctx, statement, workshopID, normalised.Limit, normalised.Offset)
	if err != nil {
		return nil, 0, wrap(err, "cannot list practice submissions")
	}
	defer func() { _ = rows.Close() }()

	out := make([]teaching.Submission, 0, normalised.Limit)
	for rows.Next() {
		submission, err := scanSubmissionRows(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, submission)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, wrap(err, "cannot iterate practice submissions")
	}
	return out, total, nil
}

// CountPendingSubmissions counts submissions still awaiting review.
func (TeachingStore) CountPendingSubmissions(ctx context.Context, q repository.Querier, workshopID int64) (int, error) {
	return countRows(ctx, q, "cannot count pending submissions",
		`SELECT COUNT(*) FROM practice_submissions WHERE workshop_id = ? AND state = 'pending'`, workshopID)
}

func scanWorkshop(row *sql.Row, key any) (teaching.Workshop, error) {
	var (
		workshop  teaching.Workshop
		state     string
		opensAt   string
		closesAt  string
		createdAt string
		updatedAt string
	)
	err := row.Scan(&workshop.ID, &workshop.StudioID, &workshop.SeriesID, &workshop.ShotID,
		&workshop.PromptVersionID, &workshop.MentorID, &workshop.Title, &workshop.Brief, &state,
		&workshop.Capacity, &workshop.Enrolled, &opensAt, &closesAt, &workshop.Version, &createdAt, &updatedAt)
	if err != nil {
		if noRows(err) {
			return teaching.Workshop{}, repository.NotFound("workshop", key)
		}
		return teaching.Workshop{}, wrap(err, "cannot read workshop")
	}
	return hydrateWorkshop(workshop, state, opensAt, closesAt, createdAt, updatedAt)
}

func scanWorkshopRows(rows *sql.Rows) (teaching.Workshop, error) {
	var (
		workshop  teaching.Workshop
		state     string
		opensAt   string
		closesAt  string
		createdAt string
		updatedAt string
	)
	err := rows.Scan(&workshop.ID, &workshop.StudioID, &workshop.SeriesID, &workshop.ShotID,
		&workshop.PromptVersionID, &workshop.MentorID, &workshop.Title, &workshop.Brief, &state,
		&workshop.Capacity, &workshop.Enrolled, &opensAt, &closesAt, &workshop.Version, &createdAt, &updatedAt)
	if err != nil {
		return teaching.Workshop{}, wrap(err, "cannot scan workshop")
	}
	return hydrateWorkshop(workshop, state, opensAt, closesAt, createdAt, updatedAt)
}

func hydrateWorkshop(workshop teaching.Workshop, state, opensAt, closesAt, createdAt, updatedAt string) (teaching.Workshop, error) {
	var err error
	if workshop.OpensAt, err = decodeTime(opensAt); err != nil {
		return teaching.Workshop{}, err
	}
	if workshop.ClosesAt, err = decodeTime(closesAt); err != nil {
		return teaching.Workshop{}, err
	}
	if workshop.CreatedAt, err = decodeTime(createdAt); err != nil {
		return teaching.Workshop{}, err
	}
	if workshop.UpdatedAt, err = decodeTime(updatedAt); err != nil {
		return teaching.Workshop{}, err
	}
	workshop.State = teaching.WorkshopState(state)
	return workshop, nil
}

func scanSubmission(row *sql.Row, key any) (teaching.Submission, error) {
	var (
		submission teaching.Submission
		state      string
		reviewedBy sql.NullInt64
		reviewedAt sql.NullString
		createdAt  string
		updatedAt  string
	)
	err := row.Scan(&submission.ID, &submission.WorkshopID, &submission.EnrollmentID, &submission.PromptVersionID,
		&submission.Body, &state, &submission.Score, &submission.Feedback, &reviewedBy, &reviewedAt,
		&submission.Version, &createdAt, &updatedAt)
	if err != nil {
		if noRows(err) {
			return teaching.Submission{}, repository.NotFound("practice submission", key)
		}
		return teaching.Submission{}, wrap(err, "cannot read practice submission")
	}
	return hydrateSubmission(submission, state, reviewedBy, reviewedAt, createdAt, updatedAt)
}

func scanSubmissionRows(rows *sql.Rows) (teaching.Submission, error) {
	var (
		submission teaching.Submission
		state      string
		reviewedBy sql.NullInt64
		reviewedAt sql.NullString
		createdAt  string
		updatedAt  string
	)
	err := rows.Scan(&submission.ID, &submission.WorkshopID, &submission.EnrollmentID, &submission.PromptVersionID,
		&submission.Body, &state, &submission.Score, &submission.Feedback, &reviewedBy, &reviewedAt,
		&submission.Version, &createdAt, &updatedAt)
	if err != nil {
		return teaching.Submission{}, wrap(err, "cannot scan practice submission")
	}
	return hydrateSubmission(submission, state, reviewedBy, reviewedAt, createdAt, updatedAt)
}

func hydrateSubmission(submission teaching.Submission, state string, reviewedBy sql.NullInt64, reviewedAt sql.NullString, createdAt, updatedAt string) (teaching.Submission, error) {
	parsedReviewed, err := decodeNullableTime(reviewedAt)
	if err != nil {
		return teaching.Submission{}, err
	}
	if submission.CreatedAt, err = decodeTime(createdAt); err != nil {
		return teaching.Submission{}, err
	}
	if submission.UpdatedAt, err = decodeTime(updatedAt); err != nil {
		return teaching.Submission{}, err
	}
	submission.State = teaching.SubmissionState(state)
	submission.ReviewedBy = decodeNullableInt64(reviewedBy)
	submission.ReviewedAt = parsedReviewed
	return submission.Clone(), nil
}
