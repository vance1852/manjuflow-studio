package sqliterepo

import (
	"context"
	"database/sql"
	"strings"

	"github.com/vance1852/manjuflow-studio/internal/domain/production"
	"github.com/vance1852/manjuflow-studio/internal/repository"
)

// SeriesStore implements repository.SeriesRepository.
type SeriesStore struct{}

// NewSeriesStore builds the series repository.
func NewSeriesStore() SeriesStore { return SeriesStore{} }

const seriesColumns = `id, studio_id, code, title, logline, state, version, created_by, created_at, updated_at, published_at`

// Create inserts a series.
func (SeriesStore) Create(ctx context.Context, q repository.Querier, series production.Series) (int64, error) {
	return insert(ctx, q, "cannot create series",
		`INSERT INTO series (studio_id, code, title, logline, state, version, created_by, created_at, updated_at, published_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		series.StudioID, series.Code, series.Title, series.Logline, string(series.State), series.Version,
		series.CreatedBy, encodeTime(series.CreatedAt), encodeTime(series.UpdatedAt),
		encodeNullableTime(series.PublishedAt))
}

// FindByID loads a series by identifier.
func (SeriesStore) FindByID(ctx context.Context, q repository.Querier, id int64) (production.Series, error) {
	row := q.QueryRowContext(ctx, `SELECT `+seriesColumns+` FROM series WHERE id = ?`, id)
	return scanSeries(row, id)
}

// FindByCode loads a series by studio scoped code.
func (SeriesStore) FindByCode(ctx context.Context, q repository.Querier, studioID int64, code string) (production.Series, error) {
	row := q.QueryRowContext(ctx,
		`SELECT `+seriesColumns+` FROM series WHERE studio_id = ? AND code = ?`, studioID, code)
	return scanSeries(row, code)
}

// Save persists a series with optimistic locking. The stored version must still
// match the value the caller loaded, otherwise the write is rejected.
func (SeriesStore) Save(ctx context.Context, q repository.Querier, series production.Series) error {
	affected, err := execExpectingRow(ctx, q, "cannot update series",
		`UPDATE series SET title = ?, logline = ?, state = ?, version = version + 1, updated_at = ?, published_at = ?
		 WHERE id = ? AND version = ?`,
		series.Title, series.Logline, string(series.State), encodeTime(series.UpdatedAt),
		encodeNullableTime(series.PublishedAt), series.ID, series.Version)
	if err != nil {
		return err
	}
	if affected == 0 {
		return staleWrite("series", series.ID, series.Version)
	}
	return nil
}

var seriesSortColumns = map[string]string{
	"created_at": "created_at",
	"updated_at": "updated_at",
	"code":       "code",
	"title":      "title",
	"state":      "state",
}

// SeriesSortColumns exposes the allowed sort keys for series listings.
func SeriesSortColumns() map[string]string { return copyColumns(seriesSortColumns) }

// List returns one page of series plus the total count matching the same filter.
func (SeriesStore) List(ctx context.Context, q repository.Querier, studioID int64, filter repository.SeriesFilter, page repository.Page) ([]production.Series, int, error) {
	normalised, err := repository.NormalisePage(page, seriesSortColumns, "created_at")
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
	if trimmed := strings.TrimSpace(filter.TitleLike); trimmed != "" {
		where = append(where, "title LIKE ?")
		args = append(args, "%"+trimmed+"%")
	}
	if filter.CreatedFrom != nil {
		where = append(where, "created_at >= ?")
		args = append(args, encodeTime(*filter.CreatedFrom))
	}
	if filter.CreatedTo != nil {
		where = append(where, "created_at <= ?")
		args = append(args, encodeTime(*filter.CreatedTo))
	}
	clause := " WHERE " + strings.Join(where, " AND ")

	total, err := countRows(ctx, q, "cannot count series", `SELECT COUNT(*) FROM series`+clause, args...)
	if err != nil {
		return nil, 0, err
	}
	statement := `SELECT ` + seriesColumns + ` FROM series` + clause +
		orderClause(repository.Column(seriesSortColumns, normalised.SortBy), normalised.Desc) +
		` LIMIT ? OFFSET ?`
	pageArgs := append(append([]any{}, args...), normalised.Limit, normalised.Offset)
	rows, err := q.QueryContext(ctx, statement, pageArgs...)
	if err != nil {
		return nil, 0, wrap(err, "cannot list series")
	}
	defer func() { _ = rows.Close() }()

	out := make([]production.Series, 0, normalised.Limit)
	for rows.Next() {
		var (
			series      production.Series
			state       string
			createdAt   string
			updatedAt   string
			publishedAt sql.NullString
		)
		if err := rows.Scan(&series.ID, &series.StudioID, &series.Code, &series.Title, &series.Logline,
			&state, &series.Version, &series.CreatedBy, &createdAt, &updatedAt, &publishedAt); err != nil {
			return nil, 0, wrap(err, "cannot scan series")
		}
		hydrated, err := hydrateSeries(series, state, createdAt, updatedAt, publishedAt)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, hydrated)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, wrap(err, "cannot iterate series")
	}
	return out, total, nil
}

func scanSeries(row *sql.Row, key any) (production.Series, error) {
	var (
		series      production.Series
		state       string
		createdAt   string
		updatedAt   string
		publishedAt sql.NullString
	)
	err := row.Scan(&series.ID, &series.StudioID, &series.Code, &series.Title, &series.Logline,
		&state, &series.Version, &series.CreatedBy, &createdAt, &updatedAt, &publishedAt)
	if err != nil {
		if noRows(err) {
			return production.Series{}, repository.NotFound("series", key)
		}
		return production.Series{}, wrap(err, "cannot read series")
	}
	return hydrateSeries(series, state, createdAt, updatedAt, publishedAt)
}

func hydrateSeries(series production.Series, state, createdAt, updatedAt string, publishedAt sql.NullString) (production.Series, error) {
	parsedCreated, err := decodeTime(createdAt)
	if err != nil {
		return production.Series{}, err
	}
	parsedUpdated, err := decodeTime(updatedAt)
	if err != nil {
		return production.Series{}, err
	}
	parsedPublished, err := decodeNullableTime(publishedAt)
	if err != nil {
		return production.Series{}, err
	}
	series.State = production.SeriesState(state)
	series.CreatedAt = parsedCreated
	series.UpdatedAt = parsedUpdated
	series.PublishedAt = parsedPublished
	return series.Clone(), nil
}

// ShotStore implements repository.ShotRepository.
type ShotStore struct{}

// NewShotStore builds the shot repository.
func NewShotStore() ShotStore { return ShotStore{} }

const shotColumns = `id, series_id, ordinal, title, direction, state, prompt_version_id, artifact_ref, rework_reason, version, created_at, updated_at`

// Create inserts a storyboard shot.
func (ShotStore) Create(ctx context.Context, q repository.Querier, shot production.Shot) (int64, error) {
	return insert(ctx, q, "cannot create shot",
		`INSERT INTO shots (series_id, ordinal, title, direction, state, prompt_version_id, artifact_ref, rework_reason, version, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		shot.SeriesID, shot.Ordinal, shot.Title, shot.Direction, string(shot.State),
		nullableInt64(shot.PromptVersionID), shot.ArtifactRef, shot.ReworkReason, shot.Version,
		encodeTime(shot.CreatedAt), encodeTime(shot.UpdatedAt))
}

// FindByID loads a shot by identifier.
func (ShotStore) FindByID(ctx context.Context, q repository.Querier, id int64) (production.Shot, error) {
	row := q.QueryRowContext(ctx, `SELECT `+shotColumns+` FROM shots WHERE id = ?`, id)
	return scanShot(row, id)
}

// FindByOrdinal loads a shot by series and ordinal.
func (ShotStore) FindByOrdinal(ctx context.Context, q repository.Querier, seriesID int64, ordinal int) (production.Shot, error) {
	row := q.QueryRowContext(ctx,
		`SELECT `+shotColumns+` FROM shots WHERE series_id = ? AND ordinal = ?`, seriesID, ordinal)
	return scanShot(row, ordinal)
}

// Save persists a shot with optimistic locking.
func (ShotStore) Save(ctx context.Context, q repository.Querier, shot production.Shot) error {
	affected, err := execExpectingRow(ctx, q, "cannot update shot",
		`UPDATE shots SET title = ?, direction = ?, state = ?, prompt_version_id = ?, artifact_ref = ?,
		 rework_reason = ?, version = version + 1, updated_at = ?
		 WHERE id = ? AND version = ?`,
		shot.Title, shot.Direction, string(shot.State), nullableInt64(shot.PromptVersionID),
		shot.ArtifactRef, shot.ReworkReason, encodeTime(shot.UpdatedAt), shot.ID, shot.Version)
	if err != nil {
		return err
	}
	if affected == 0 {
		return staleWrite("shot", shot.ID, shot.Version)
	}
	return nil
}

// ListBySeries returns every shot of a series ordered by storyboard position.
func (ShotStore) ListBySeries(ctx context.Context, q repository.Querier, seriesID int64) ([]production.Shot, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+shotColumns+` FROM shots WHERE series_id = ? ORDER BY ordinal ASC`, seriesID)
	if err != nil {
		return nil, wrap(err, "cannot list shots")
	}
	defer func() { _ = rows.Close() }()

	var out []production.Shot
	for rows.Next() {
		shot, err := scanShotRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, shot)
	}
	if err := rows.Err(); err != nil {
		return nil, wrap(err, "cannot iterate shots")
	}
	return out, nil
}

// CountReferencingPromptVersion counts shots that still point at a version and
// are not finished with it yet.
func (ShotStore) CountReferencingPromptVersion(ctx context.Context, q repository.Querier, promptVersionID int64) (int, error) {
	return countRows(ctx, q, "cannot count prompt version references",
		`SELECT COUNT(*) FROM shots WHERE prompt_version_id = ? AND state IN ('bound', 'rendering', 'rendered')`,
		promptVersionID)
}

func scanShot(row *sql.Row, key any) (production.Shot, error) {
	var (
		shot      production.Shot
		state     string
		promptID  sql.NullInt64
		createdAt string
		updatedAt string
	)
	err := row.Scan(&shot.ID, &shot.SeriesID, &shot.Ordinal, &shot.Title, &shot.Direction, &state,
		&promptID, &shot.ArtifactRef, &shot.ReworkReason, &shot.Version, &createdAt, &updatedAt)
	if err != nil {
		if noRows(err) {
			return production.Shot{}, repository.NotFound("shot", key)
		}
		return production.Shot{}, wrap(err, "cannot read shot")
	}
	return hydrateShot(shot, state, promptID, createdAt, updatedAt)
}

func scanShotRows(rows *sql.Rows) (production.Shot, error) {
	var (
		shot      production.Shot
		state     string
		promptID  sql.NullInt64
		createdAt string
		updatedAt string
	)
	err := rows.Scan(&shot.ID, &shot.SeriesID, &shot.Ordinal, &shot.Title, &shot.Direction, &state,
		&promptID, &shot.ArtifactRef, &shot.ReworkReason, &shot.Version, &createdAt, &updatedAt)
	if err != nil {
		return production.Shot{}, wrap(err, "cannot scan shot")
	}
	return hydrateShot(shot, state, promptID, createdAt, updatedAt)
}

func hydrateShot(shot production.Shot, state string, promptID sql.NullInt64, createdAt, updatedAt string) (production.Shot, error) {
	parsedCreated, err := decodeTime(createdAt)
	if err != nil {
		return production.Shot{}, err
	}
	parsedUpdated, err := decodeTime(updatedAt)
	if err != nil {
		return production.Shot{}, err
	}
	shot.State = production.ShotState(state)
	shot.PromptVersionID = decodeNullableInt64(promptID)
	shot.CreatedAt = parsedCreated
	shot.UpdatedAt = parsedUpdated
	return shot.Clone(), nil
}
