// Package service implements search business logic: it builds engine queries from the
// public search API and delegates to a SearchEngine (OpenSearch in prod, a Postgres-backed
// stub in dev/tests). It owns no transactional writes of its own.
package service

import (
	"context"

	"github.com/aizorix/platform/search/internal/store"
)

// Service delegates queries to a SearchEngine. st is retained for any direct Postgres
// fallbacks (e.g. typeahead suggestions) the engine does not cover.
type Service struct {
	store *store.Store
	es    store.SearchEngine
}

// New wires the store (Postgres) and the search engine (OpenSearch or stub).
func New(st *store.Store, es store.SearchEngine) *Service {
	return &Service{store: st, es: es}
}

// ProjectFilters are the optional structured filters for a project search.
type ProjectFilters struct {
	BudgetType string
	MinBudget  int64
	MaxBudget  int64
}

// SearchProjects delegates a project query to the engine's "projects" index.
func (s *Service) SearchProjects(ctx context.Context, q string, f ProjectFilters, limit, offset int) (*store.Result, error) {
	filters := map[string]any{}
	if f.BudgetType != "" {
		filters["budget_type"] = f.BudgetType
	}
	if f.MinBudget > 0 {
		filters["min_budget"] = f.MinBudget
	}
	if f.MaxBudget > 0 {
		filters["max_budget"] = f.MaxBudget
	}
	return s.es.Search(ctx, "projects", store.Query{
		Text:    q,
		Filters: filters,
		Limit:   limit,
		Offset:  offset,
	})
}

// SearchFreelancers delegates a freelancer query to the engine's "freelancers" index.
// Freelancer documents are indexed asynchronously; the dev stub returns an empty page.
func (s *Service) SearchFreelancers(ctx context.Context, q string, skills []string, limit, offset int) (*store.Result, error) {
	filters := map[string]any{}
	if len(skills) > 0 {
		filters["skills"] = skills
	}
	return s.es.Search(ctx, "freelancers", store.Query{
		Text:    q,
		Filters: filters,
		Limit:   limit,
		Offset:  offset,
	})
}

// IndexProject upserts a project document into the engine. Index consumers run as a
// separate Kafka consumer; they call SearchEngine.Index on project.published/profile.updated
// events. This method exists so those consumers (and tests) have a typed entry point.
func (s *Service) IndexProject(ctx context.Context, projectID string, doc map[string]any) error {
	return s.es.Index(ctx, "projects", projectID, doc)
}

// indexForKind maps a logical document kind (as the index consumer knows it) to the engine
// index name. Unknown kinds map to "" so the caller can no-op rather than touch a bad index.
func indexForKind(kind string) string {
	switch kind {
	case "project", "projects":
		return "projects"
	case "freelancer", "freelancers", "profile", "user":
		return "freelancers"
	default:
		return ""
	}
}

// Index upserts a document of the given kind (e.g. "project", "freelancer") into the engine's
// corresponding index. It is the generic entry point used by the index consumer so it does not
// need to know engine index names. Unknown kinds are a no-op.
func (s *Service) Index(ctx context.Context, kind, id string, doc map[string]any) error {
	index := indexForKind(kind)
	if index == "" || id == "" {
		return nil
	}
	return s.es.Index(ctx, index, id, doc)
}

// Delete removes a document of the given kind from the engine's corresponding index (e.g. when
// a project is closed/unpublished). Unknown kinds are a no-op.
func (s *Service) Delete(ctx context.Context, kind, id string) error {
	index := indexForKind(kind)
	if index == "" || id == "" {
		return nil
	}
	return s.es.Delete(ctx, index, id)
}

// Suggest returns typeahead suggestions for the given prefix. The stub serves project
// titles from Postgres via ILIKE.
func (s *Service) Suggest(ctx context.Context, prefix string) ([]string, error) {
	if prefix == "" {
		return []string{}, nil
	}
	return s.store.SuggestProjectTitles(ctx, prefix)
}
