// Package promptsvc owns the prompt library: templates, immutable versions and
// the cross entity rules that decide when a version may be retired.
package promptsvc

import (
	"context"
	"strconv"
	"strings"

	"github.com/vance1852/manjuflow-studio/internal/apperr"
	"github.com/vance1852/manjuflow-studio/internal/auditlog"
	"github.com/vance1852/manjuflow-studio/internal/clock"
	"github.com/vance1852/manjuflow-studio/internal/domain/audit"
	"github.com/vance1852/manjuflow-studio/internal/domain/identity"
	"github.com/vance1852/manjuflow-studio/internal/domain/prompt"
	"github.com/vance1852/manjuflow-studio/internal/repository"
	"github.com/vance1852/manjuflow-studio/internal/reqctx"
)

// Service implements the prompt library use cases.
type Service struct {
	runner   repository.Runner
	prompts  repository.PromptRepository
	shots    repository.ShotRepository
	renders  repository.RenderRepository
	teaching repository.TeachingRepository
	audits   *auditlog.Recorder
	clock    clock.Clock
}

// Options configures the service.
type Options struct {
	Runner   repository.Runner
	Prompts  repository.PromptRepository
	Shots    repository.ShotRepository
	Renders  repository.RenderRepository
	Teaching repository.TeachingRepository
	Audits   *auditlog.Recorder
	Clock    clock.Clock
}

// New builds the prompt service.
func New(opts Options) *Service {
	return &Service{
		runner:   opts.Runner,
		prompts:  opts.Prompts,
		shots:    opts.Shots,
		renders:  opts.Renders,
		teaching: opts.Teaching,
		audits:   opts.Audits,
		clock:    opts.Clock,
	}
}

// CreateTemplateInput describes a new prompt template.
type CreateTemplateInput struct {
	Slug       string
	Title      string
	Discipline string
}

// CreateTemplate registers a named prompt asset.
func (s *Service) CreateTemplate(ctx context.Context, input CreateTemplateInput) (prompt.Template, error) {
	principal, err := s.authorize(ctx, identity.CapManagePromptAssets)
	if err != nil {
		return prompt.Template{}, err
	}
	slug, err := prompt.NormaliseSlug(input.Slug)
	if err != nil {
		return prompt.Template{}, err
	}
	title, err := prompt.ValidateTitle(input.Title)
	if err != nil {
		return prompt.Template{}, err
	}
	now := s.clock.Now()
	template := prompt.Template{
		StudioID:   principal.StudioID,
		OwnerID:    principal.UserID,
		Slug:       slug,
		Title:      title,
		Discipline: strings.TrimSpace(input.Discipline),
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	err = s.runner.InTx(ctx, func(ctx context.Context, q repository.Querier) error {
		id, err := s.prompts.CreateTemplate(ctx, q, template)
		if err != nil {
			if apperr.IsCode(err, apperr.CodeConflict) {
				return apperr.New(apperr.CodeConflict, "prompt template %q already exists", slug).With("field", "slug")
			}
			return err
		}
		template.ID = id
		return s.audits.Record(ctx, q, auditlog.Success("prompt_template.created", "prompt_template", id).
			WithDetail("slug", slug))
	})
	if err != nil {
		return prompt.Template{}, err
	}
	return template, nil
}

// AppendVersionInput describes a new immutable revision.
type AppendVersionInput struct {
	TemplateID int64
	Body       string
	Notes      string
	Activate   bool
}

// AppendVersion adds the next revision of a template. Version numbers are
// derived from the stored head so two concurrent appends cannot collide.
func (s *Service) AppendVersion(ctx context.Context, input AppendVersionInput) (prompt.Version, error) {
	principal, err := s.authorize(ctx, identity.CapManagePromptAssets)
	if err != nil {
		return prompt.Version{}, err
	}
	body, err := prompt.ValidateBody(input.Body)
	if err != nil {
		return prompt.Version{}, err
	}
	notes, err := prompt.ValidateNotes(input.Notes)
	if err != nil {
		return prompt.Version{}, err
	}
	now := s.clock.Now()
	var created prompt.Version
	err = s.runner.InTx(ctx, func(ctx context.Context, q repository.Querier) error {
		template, err := s.prompts.FindTemplateByID(ctx, q, input.TemplateID)
		if err != nil {
			return err
		}
		if template.StudioID != principal.StudioID {
			return apperr.New(apperr.CodeNotFound, "prompt template not found").With("entity", "prompt template")
		}
		next := template.HeadVersion + 1
		version := prompt.Version{
			TemplateID: template.ID,
			Version:    next,
			Body:       body,
			Checksum:   prompt.Checksum(body),
			Status:     prompt.StatusDraft,
			Notes:      notes,
			CreatedBy:  principal.UserID,
			CreatedAt:  now,
		}
		if input.Activate {
			if err := version.Activate(now); err != nil {
				return err
			}
		}
		id, err := s.prompts.CreateVersion(ctx, q, version)
		if err != nil {
			return err
		}
		version.ID = id
		if err := s.prompts.AdvanceTemplateHead(ctx, q, template.ID, next, now); err != nil {
			return err
		}
		created = version
		return s.audits.Record(ctx, q, auditlog.Success("prompt_version.appended", audit.ObjectPromptVersion, id).
			WithDetail("template_slug", template.Slug).
			WithDetail("version", strconv.Itoa(next)).
			WithDetail("status", string(version.Status)))
	})
	if err != nil {
		return prompt.Version{}, err
	}
	return created, nil
}

// ActivateVersion freezes a draft version so it can enter production.
func (s *Service) ActivateVersion(ctx context.Context, versionID int64) (prompt.Version, error) {
	principal, err := s.authorize(ctx, identity.CapManagePromptAssets)
	if err != nil {
		return prompt.Version{}, err
	}
	now := s.clock.Now()
	var updated prompt.Version
	err = s.runner.InTx(ctx, func(ctx context.Context, q repository.Querier) error {
		version, template, err := s.loadVersion(ctx, q, versionID, principal.StudioID)
		if err != nil {
			return err
		}
		if err := version.VerifyIntegrity(); err != nil {
			return err
		}
		if err := version.Activate(now); err != nil {
			return err
		}
		if err := s.prompts.SaveVersionStatus(ctx, q, version); err != nil {
			return err
		}
		updated = version
		return s.audits.Record(ctx, q, auditlog.Success("prompt_version.activated", audit.ObjectPromptVersion, version.ID).
			WithDetail("template_slug", template.Slug).
			WithDetail("version", strconv.Itoa(version.Version)))
	})
	if err != nil {
		return prompt.Version{}, err
	}
	return updated, nil
}

// RetireVersion withdraws a version. Live references across storyboard shots,
// unfinished render jobs and open teaching workshops all block the retirement,
// because those flows still need the exact wording.
func (s *Service) RetireVersion(ctx context.Context, versionID int64) (prompt.Version, error) {
	principal, err := s.authorize(ctx, identity.CapManagePromptAssets)
	if err != nil {
		return prompt.Version{}, err
	}
	now := s.clock.Now()
	var (
		updated   prompt.Version
		rejection audit.Event
	)
	err = s.runner.InTx(ctx, func(ctx context.Context, q repository.Querier) error {
		version, template, err := s.loadVersion(ctx, q, versionID, principal.StudioID)
		if err != nil {
			return err
		}
		references, err := s.countLiveReferences(ctx, q, version.ID)
		if err != nil {
			return err
		}
		if err := version.Retire(now, references.total()); err != nil {
			rejection = auditlog.Rejected("prompt_version.retire_blocked", audit.ObjectPromptVersion, version.ID).
				WithDetail("bound_shots", strconv.Itoa(references.Shots)).
				WithDetail("unfinished_jobs", strconv.Itoa(references.Jobs)).
				WithDetail("live_workshops", strconv.Itoa(references.Workshops))
			rejection.StudioID = principal.StudioID
			rejection.ActorID = principal.UserID
			rejection.ActorRole = string(principal.Role)
			return err
		}
		if err := s.prompts.SaveVersionStatus(ctx, q, version); err != nil {
			return err
		}
		updated = version
		return s.audits.Record(ctx, q, auditlog.Success("prompt_version.retired", audit.ObjectPromptVersion, version.ID).
			WithDetail("template_slug", template.Slug).
			WithDetail("version", strconv.Itoa(version.Version)))
	})
	if err != nil {
		s.recordRejection(ctx, rejection)
		return prompt.Version{}, err
	}
	return updated, nil
}

// recordRejection persists a refused attempt in its own transaction. The business
// transaction rolled back, so the trail would otherwise lose every rule that
// protected the library.
func (s *Service) recordRejection(ctx context.Context, event audit.Event) {
	if event.Action == "" {
		return
	}
	_ = s.runner.InTx(context.WithoutCancel(ctx), func(ctx context.Context, q repository.Querier) error {
		return s.audits.Record(ctx, q, event)
	})
}

// References counts the live users of one prompt version.
type References struct {
	Shots     int
	Jobs      int
	Workshops int
}

func (r References) total() int { return r.Shots + r.Jobs + r.Workshops }

// Total exposes the aggregated reference count.
func (r References) Total() int { return r.total() }

// CountReferences reports which flows still hold a prompt version.
func (s *Service) CountReferences(ctx context.Context, versionID int64) (References, error) {
	principal, err := s.authorize(ctx, identity.CapViewCatalog)
	if err != nil {
		return References{}, err
	}
	var out References
	err = s.runner.InTx(ctx, func(ctx context.Context, q repository.Querier) error {
		if _, _, err := s.loadVersion(ctx, q, versionID, principal.StudioID); err != nil {
			return err
		}
		counted, err := s.countLiveReferences(ctx, q, versionID)
		if err != nil {
			return err
		}
		out = counted
		return nil
	})
	if err != nil {
		return References{}, err
	}
	return out, nil
}

// ListVersions returns one page of the revision chain.
func (s *Service) ListVersions(ctx context.Context, templateID int64, page repository.Page) ([]prompt.Version, int, error) {
	principal, err := s.authorize(ctx, identity.CapViewCatalog)
	if err != nil {
		return nil, 0, err
	}
	var (
		versions []prompt.Version
		total    int
	)
	err = s.runner.InTx(ctx, func(ctx context.Context, q repository.Querier) error {
		template, err := s.prompts.FindTemplateByID(ctx, q, templateID)
		if err != nil {
			return err
		}
		if template.StudioID != principal.StudioID {
			return apperr.New(apperr.CodeNotFound, "prompt template not found").With("entity", "prompt template")
		}
		versions, total, err = s.prompts.ListVersions(ctx, q, templateID, page)
		return err
	})
	if err != nil {
		return nil, 0, err
	}
	return versions, total, nil
}

// FindTemplate loads one template of the caller studio.
func (s *Service) FindTemplate(ctx context.Context, slug string) (prompt.Template, error) {
	principal, err := s.authorize(ctx, identity.CapViewCatalog)
	if err != nil {
		return prompt.Template{}, err
	}
	normalised, err := prompt.NormaliseSlug(slug)
	if err != nil {
		return prompt.Template{}, err
	}
	var template prompt.Template
	err = s.runner.InTx(ctx, func(ctx context.Context, q repository.Querier) error {
		found, err := s.prompts.FindTemplateBySlug(ctx, q, principal.StudioID, normalised)
		if err != nil {
			return err
		}
		template = found
		return nil
	})
	if err != nil {
		return prompt.Template{}, err
	}
	return template, nil
}

func (s *Service) countLiveReferences(ctx context.Context, q repository.Querier, versionID int64) (References, error) {
	shots, err := s.shots.CountReferencingPromptVersion(ctx, q, versionID)
	if err != nil {
		return References{}, err
	}
	jobs, err := s.renders.CountUnfinishedForPromptVersion(ctx, q, versionID)
	if err != nil {
		return References{}, err
	}
	workshops, err := s.teaching.CountLiveWorkshopsForPromptVersion(ctx, q, versionID)
	if err != nil {
		return References{}, err
	}
	return References{Shots: shots, Jobs: jobs, Workshops: workshops}, nil
}

func (s *Service) loadVersion(ctx context.Context, q repository.Querier, versionID, studioID int64) (prompt.Version, prompt.Template, error) {
	version, err := s.prompts.FindVersionByID(ctx, q, versionID)
	if err != nil {
		return prompt.Version{}, prompt.Template{}, err
	}
	template, err := s.prompts.FindTemplateByID(ctx, q, version.TemplateID)
	if err != nil {
		return prompt.Version{}, prompt.Template{}, err
	}
	if template.StudioID != studioID {
		return prompt.Version{}, prompt.Template{},
			apperr.New(apperr.CodeNotFound, "prompt version not found").With("entity", "prompt version")
	}
	return version, template, nil
}

func (s *Service) authorize(ctx context.Context, capability identity.Capability) (identity.Principal, error) {
	principal, err := reqctx.RequirePrincipal(ctx)
	if err != nil {
		return identity.Principal{}, err
	}
	if err := principal.Authorize(capability); err != nil {
		return identity.Principal{}, err
	}
	return principal, nil
}
