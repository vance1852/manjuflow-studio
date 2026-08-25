package sqliterepo

import (
	"context"
	"database/sql"
	"time"

	"github.com/vance1852/manjuflow-studio/internal/domain/prompt"
	"github.com/vance1852/manjuflow-studio/internal/repository"
)

// PromptStore implements repository.PromptRepository.
type PromptStore struct{}

// NewPromptStore builds the prompt repository.
func NewPromptStore() PromptStore { return PromptStore{} }

const templateColumns = `id, studio_id, owner_id, slug, title, discipline, head_version, created_at, updated_at`

// CreateTemplate inserts a prompt template.
func (PromptStore) CreateTemplate(ctx context.Context, q repository.Querier, template prompt.Template) (int64, error) {
	return insert(ctx, q, "cannot create prompt template",
		`INSERT INTO prompt_templates (studio_id, owner_id, slug, title, discipline, head_version, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		template.StudioID, template.OwnerID, template.Slug, template.Title, template.Discipline,
		template.HeadVersion, encodeTime(template.CreatedAt), encodeTime(template.UpdatedAt))
}

// FindTemplateByID loads a template by identifier.
func (PromptStore) FindTemplateByID(ctx context.Context, q repository.Querier, id int64) (prompt.Template, error) {
	row := q.QueryRowContext(ctx, `SELECT `+templateColumns+` FROM prompt_templates WHERE id = ?`, id)
	return scanTemplate(row, id)
}

// FindTemplateBySlug loads a template by studio scoped slug.
func (PromptStore) FindTemplateBySlug(ctx context.Context, q repository.Querier, studioID int64, slug string) (prompt.Template, error) {
	row := q.QueryRowContext(ctx,
		`SELECT `+templateColumns+` FROM prompt_templates WHERE studio_id = ? AND slug = ?`, studioID, slug)
	return scanTemplate(row, slug)
}

func scanTemplate(row *sql.Row, key any) (prompt.Template, error) {
	var (
		template  prompt.Template
		createdAt string
		updatedAt string
	)
	err := row.Scan(&template.ID, &template.StudioID, &template.OwnerID, &template.Slug, &template.Title,
		&template.Discipline, &template.HeadVersion, &createdAt, &updatedAt)
	if err != nil {
		if noRows(err) {
			return prompt.Template{}, repository.NotFound("prompt template", key)
		}
		return prompt.Template{}, wrap(err, "cannot read prompt template")
	}
	if template.CreatedAt, err = decodeTime(createdAt); err != nil {
		return prompt.Template{}, err
	}
	if template.UpdatedAt, err = decodeTime(updatedAt); err != nil {
		return prompt.Template{}, err
	}
	return template, nil
}

const versionColumns = `id, template_id, version, body, checksum, status, notes, created_by, created_at, retired_at`

// CreateVersion appends an immutable prompt version.
func (PromptStore) CreateVersion(ctx context.Context, q repository.Querier, version prompt.Version) (int64, error) {
	return insert(ctx, q, "cannot create prompt version",
		`INSERT INTO prompt_versions (template_id, version, body, checksum, status, notes, created_by, created_at, retired_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		version.TemplateID, version.Version, version.Body, version.Checksum, string(version.Status),
		version.Notes, version.CreatedBy, encodeTime(version.CreatedAt), encodeNullableTime(version.RetiredAt))
}

// FindVersionByID loads a version by identifier.
func (PromptStore) FindVersionByID(ctx context.Context, q repository.Querier, id int64) (prompt.Version, error) {
	row := q.QueryRowContext(ctx, `SELECT `+versionColumns+` FROM prompt_versions WHERE id = ?`, id)
	return scanVersion(row, id)
}

// FindVersion loads a version by template and number.
func (PromptStore) FindVersion(ctx context.Context, q repository.Querier, templateID int64, number int) (prompt.Version, error) {
	row := q.QueryRowContext(ctx,
		`SELECT `+versionColumns+` FROM prompt_versions WHERE template_id = ? AND version = ?`, templateID, number)
	return scanVersion(row, number)
}

// SaveVersionStatus persists a lifecycle change of one version. The body itself
// is immutable and never rewritten.
func (PromptStore) SaveVersionStatus(ctx context.Context, q repository.Querier, version prompt.Version) error {
	affected, err := execExpectingRow(ctx, q, "cannot update prompt version",
		`UPDATE prompt_versions SET status = ?, retired_at = ?, notes = ? WHERE id = ?`,
		string(version.Status), encodeNullableTime(version.RetiredAt), version.Notes, version.ID)
	if err != nil {
		return err
	}
	if affected == 0 {
		return repository.NotFound("prompt version", version.ID)
	}
	return nil
}

// AdvanceTemplateHead moves the head pointer forward. It never moves backwards,
// so a concurrent append cannot lower the recorded head.
func (PromptStore) AdvanceTemplateHead(ctx context.Context, q repository.Querier, templateID int64, head int, now time.Time) error {
	affected, err := execExpectingRow(ctx, q, "cannot advance template head",
		`UPDATE prompt_templates SET head_version = ?, updated_at = ?
		 WHERE id = ? AND head_version < ?`, head, encodeTime(now), templateID, head)
	if err != nil {
		return err
	}
	if affected == 0 {
		return repository.NotFound("prompt template head below", head)
	}
	return nil
}

var versionSortColumns = map[string]string{
	"version":    "version",
	"created_at": "created_at",
	"status":     "status",
}

// VersionSortColumns exposes the allowed sort keys for version listings.
func VersionSortColumns() map[string]string { return copyColumns(versionSortColumns) }

// ListVersions returns one page of versions plus the total count.
func (PromptStore) ListVersions(ctx context.Context, q repository.Querier, templateID int64, page repository.Page) ([]prompt.Version, int, error) {
	normalised, err := repository.NormalisePage(page, versionSortColumns, "version")
	if err != nil {
		return nil, 0, err
	}
	total, err := countRows(ctx, q, "cannot count prompt versions",
		`SELECT COUNT(*) FROM prompt_versions WHERE template_id = ?`, templateID)
	if err != nil {
		return nil, 0, err
	}
	statement := `SELECT ` + versionColumns + ` FROM prompt_versions WHERE template_id = ?` +
		orderClause(repository.Column(versionSortColumns, normalised.SortBy), normalised.Desc) +
		` LIMIT ? OFFSET ?`
	rows, err := q.QueryContext(ctx, statement, templateID, normalised.Limit, normalised.Offset)
	if err != nil {
		return nil, 0, wrap(err, "cannot list prompt versions")
	}
	defer func() { _ = rows.Close() }()

	out := make([]prompt.Version, 0, normalised.Limit)
	for rows.Next() {
		version, err := scanVersionRows(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, version)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, wrap(err, "cannot iterate prompt versions")
	}
	return out, total, nil
}

func scanVersion(row *sql.Row, key any) (prompt.Version, error) {
	var (
		version   prompt.Version
		status    string
		createdAt string
		retiredAt sql.NullString
	)
	err := row.Scan(&version.ID, &version.TemplateID, &version.Version, &version.Body, &version.Checksum,
		&status, &version.Notes, &version.CreatedBy, &createdAt, &retiredAt)
	if err != nil {
		if noRows(err) {
			return prompt.Version{}, repository.NotFound("prompt version", key)
		}
		return prompt.Version{}, wrap(err, "cannot read prompt version")
	}
	return hydrateVersion(version, status, createdAt, retiredAt)
}

func scanVersionRows(rows *sql.Rows) (prompt.Version, error) {
	var (
		version   prompt.Version
		status    string
		createdAt string
		retiredAt sql.NullString
	)
	err := rows.Scan(&version.ID, &version.TemplateID, &version.Version, &version.Body, &version.Checksum,
		&status, &version.Notes, &version.CreatedBy, &createdAt, &retiredAt)
	if err != nil {
		return prompt.Version{}, wrap(err, "cannot scan prompt version")
	}
	return hydrateVersion(version, status, createdAt, retiredAt)
}

func hydrateVersion(version prompt.Version, status, createdAt string, retiredAt sql.NullString) (prompt.Version, error) {
	parsedCreated, err := decodeTime(createdAt)
	if err != nil {
		return prompt.Version{}, err
	}
	parsedRetired, err := decodeNullableTime(retiredAt)
	if err != nil {
		return prompt.Version{}, err
	}
	version.Status = prompt.VersionStatus(status)
	version.CreatedAt = parsedCreated
	version.RetiredAt = parsedRetired
	return version.Clone(), nil
}

func copyColumns(source map[string]string) map[string]string {
	out := make(map[string]string, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}
