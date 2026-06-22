// Package store is the search data layer. The search service exposes a query API that
// delegates to OpenSearch via the SearchEngine interface; in dev/tests we use
// OpenSearchStub, which falls back to Postgres full-text search over the projects table.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("store: not found")

// Query is an abstract search request handed to a SearchEngine. Filters are optional and
// engine-specific; the stub interprets a known subset for the projects index.
type Query struct {
	Text    string
	Filters map[string]any
	Limit   int
	Offset  int
}

// Hit is a single search result document.
type Hit struct {
	ID     string         `json:"id"`
	Score  float64        `json:"score"`
	Source map[string]any `json:"source"`
}

// Result is a page of hits plus the total match count.
type Result struct {
	Hits  []Hit `json:"hits"`
	Total int   `json:"total"`
}

// SearchEngine abstracts OpenSearch so we can stub it in dev/tests and swap the client.
// Index upserts a document into an index; Delete removes one by id. The index consumer
// (cmd/consumer) drives both from project.events / user.events.
type SearchEngine interface {
	Search(ctx context.Context, index string, q Query) (*Result, error)
	Index(ctx context.Context, index, id string, doc map[string]any) error
	Delete(ctx context.Context, index, id string) error
}

// Store holds the pool and implements the Postgres full-text fallback used by the stub.
type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store  { return &Store{pool: pool} }
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// SearchProjects runs a Postgres full-text search over the projects table using the
// search_tsv tsvector column. Only published, non-deleted projects are returned, ordered
// by ts_rank. It returns the page of hits plus the total number of matches.
func (s *Store) SearchProjects(ctx context.Context, q string, limit, offset int) ([]Hit, int, error) {
	if limit <= 0 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, client_id, title, description, budget_type,
		       budget_min_cents, budget_max_cents, status, published_at,
		       ts_rank(search_tsv, plainto_tsquery('english', $1)) AS rank,
		       count(*) OVER() AS total
		FROM projects
		WHERE status = 'published'
		  AND deleted_at IS NULL
		  AND search_tsv @@ plainto_tsquery('english', $1)
		ORDER BY rank DESC
		LIMIT $2 OFFSET $3`, q, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var hits []Hit
	total := 0
	for rows.Next() {
		var (
			id, clientID, title, description, budgetType, status string
			budgetMin, budgetMax                                 *int64
			publishedAt                                          *string
			rank                                                 float64
		)
		if err := rows.Scan(&id, &clientID, &title, &description, &budgetType,
			&budgetMin, &budgetMax, &status, &publishedAt, &rank, &total); err != nil {
			return nil, 0, err
		}
		hits = append(hits, Hit{
			ID:    id,
			Score: rank,
			Source: map[string]any{
				"id":               id,
				"client_id":        clientID,
				"title":            title,
				"description":      description,
				"budget_type":      budgetType,
				"budget_min_cents": budgetMin,
				"budget_max_cents": budgetMax,
				"status":           status,
				"published_at":     publishedAt,
			},
		})
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return hits, total, nil
}

// SuggestProjectTitles returns up to 10 project titles whose title matches the prefix
// (typeahead). Only published, non-deleted projects are considered.
func (s *Store) SuggestProjectTitles(ctx context.Context, prefix string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT title
		FROM projects
		WHERE status = 'published'
		  AND deleted_at IS NULL
		  AND title ILIKE $1
		ORDER BY published_at DESC NULLS LAST
		LIMIT 10`, prefix+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]string, 0, 10)
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// OpenSearchStub implements SearchEngine for dev/tests. For the "projects" index it falls
// back to Postgres full-text search via the embedded pool; for "freelancers" it returns an
// empty result (freelancer docs are indexed asynchronously by the index consumer). Indexing
// is a no-op log line — there is no real OpenSearch client wired here.
type OpenSearchStub struct {
	pool   *pgxpool.Pool
	url    string
	logger *slog.Logger
}

// NewOpenSearchStub builds the stub. It holds the pool so its projects search can run the
// SQL full-text fallback directly. url is the (currently unused) OpenSearch endpoint.
func NewOpenSearchStub(pool *pgxpool.Pool, url string, logger *slog.Logger) *OpenSearchStub {
	return &OpenSearchStub{pool: pool, url: url, logger: logger}
}

// Search delegates the projects index to Postgres FTS; other indexes return empty.
func (e *OpenSearchStub) Search(ctx context.Context, index string, q Query) (*Result, error) {
	switch index {
	case "projects":
		hits, total, err := e.searchProjects(ctx, q)
		if err != nil {
			return nil, err
		}
		return &Result{Hits: hits, Total: total}, nil
	default:
		// freelancers and any other index are populated asynchronously into OpenSearch;
		// the stub has no local fallback, so it returns an empty page.
		return &Result{Hits: []Hit{}, Total: 0}, nil
	}
}

// searchProjects runs the same FTS query as Store.SearchProjects but honors the engine
// filters (budget_type, min/max budget) passed through Query.Filters.
func (e *OpenSearchStub) searchProjects(ctx context.Context, q Query) ([]Hit, int, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 20
	}
	offset := q.Offset
	if offset < 0 {
		offset = 0
	}

	args := []any{q.Text}
	where := ` WHERE status = 'published'
		  AND deleted_at IS NULL
		  AND search_tsv @@ plainto_tsquery('english', $1)`
	if bt, ok := q.Filters["budget_type"].(string); ok && bt != "" {
		args = append(args, bt)
		where += " AND budget_type = $" + itoa(len(args))
	}
	if mn, ok := q.Filters["min_budget"].(int64); ok && mn > 0 {
		args = append(args, mn)
		where += " AND budget_min_cents >= $" + itoa(len(args))
	}
	if mx, ok := q.Filters["max_budget"].(int64); ok && mx > 0 {
		args = append(args, mx)
		where += " AND budget_max_cents <= $" + itoa(len(args))
	}
	args = append(args, limit)
	limPlaceholder := "$" + itoa(len(args))
	args = append(args, offset)
	offPlaceholder := "$" + itoa(len(args))

	sql := `
		SELECT id, client_id, title, description, budget_type,
		       budget_min_cents, budget_max_cents, status, published_at,
		       ts_rank(search_tsv, plainto_tsquery('english', $1)) AS rank,
		       count(*) OVER() AS total
		FROM projects` + where + `
		ORDER BY rank DESC
		LIMIT ` + limPlaceholder + ` OFFSET ` + offPlaceholder

	rows, err := e.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var hits []Hit
	total := 0
	for rows.Next() {
		var (
			id, clientID, title, description, budgetType, status string
			budgetMin, budgetMax                                 *int64
			publishedAt                                          *string
			rank                                                 float64
		)
		if err := rows.Scan(&id, &clientID, &title, &description, &budgetType,
			&budgetMin, &budgetMax, &status, &publishedAt, &rank, &total); err != nil {
			return nil, 0, err
		}
		hits = append(hits, Hit{
			ID:    id,
			Score: rank,
			Source: map[string]any{
				"id":               id,
				"client_id":        clientID,
				"title":            title,
				"description":      description,
				"budget_type":      budgetType,
				"budget_min_cents": budgetMin,
				"budget_max_cents": budgetMax,
				"status":           status,
				"published_at":     publishedAt,
			},
		})
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return hits, total, nil
}

// Index is a no-op in the stub: it logs the document that a real client would upsert into
// OpenSearch. Index consumers run as a separate Kafka consumer; they call SearchEngine.Index
// on project.published/profile.updated events.
func (e *OpenSearchStub) Index(ctx context.Context, index, id string, doc map[string]any) error {
	body, _ := json.Marshal(doc)
	if e.logger != nil {
		e.logger.Info("opensearch stub index", "index", index, "id", id, "doc", string(body))
	}
	return nil
}

// Delete is a no-op in the stub: it logs the id a real client would remove from OpenSearch.
// For the "projects" index the authoritative copy still lives in Postgres (the FTS fallback
// only returns published, non-deleted rows), so a stub delete has no effect on search results;
// it exists so the index consumer can drive removals against the real engine in prod.
func (e *OpenSearchStub) Delete(ctx context.Context, index, id string) error {
	if e.logger != nil {
		e.logger.Info("opensearch stub delete", "index", index, "id", id)
	}
	return nil
}

// itoa is a tiny strconv.Itoa wrapper kept local so callers building placeholder strings
// don't each import strconv.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// asNotFound maps pgx.ErrNoRows to the package ErrNotFound for callers that do single-row
// lookups.
func asNotFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

var _ = asNotFound // reserved for future single-row lookups
