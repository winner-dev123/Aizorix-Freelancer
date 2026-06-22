// This file provides the REAL OpenSearch-backed implementation of SearchEngine, used in
// production when ELASTICSEARCH_URL (or OPENSEARCH_URL) is configured. It is additive: the
// OpenSearchStub in store.go remains the default for dev/tests, and the SearchEngine interface,
// the consumer (cmd/consumer), and the handlers are all unchanged.
//
// Selection happens in NewSearchEngine (called from cmd/server and cmd/consumer wiring): when a
// URL is present we return *OpenSearch; otherwise the Postgres-FTS *OpenSearchStub. Both satisfy
// SearchEngine, so callers are agnostic to which is in use.
package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync"

	opensearch "github.com/opensearch-project/opensearch-go/v2"
	"github.com/opensearch-project/opensearch-go/v2/opensearchapi"
	"github.com/jackc/pgx/v5/pgxpool"
)

// OpenSearch is the production SearchEngine. It talks to a real OpenSearch cluster via the
// official client. Indices are created lazily on first write (Index) using the mappings defined
// below; ensureIndex guards that with a per-index once so concurrent writes don't race on
// creation. It satisfies the same SearchEngine interface as OpenSearchStub.
type OpenSearch struct {
	client *opensearch.Client
	logger *slog.Logger

	mu      sync.Mutex
	ensured map[string]bool
}

// compile-time assertion that *OpenSearch satisfies SearchEngine.
var _ SearchEngine = (*OpenSearch)(nil)

// NewOpenSearch builds a real OpenSearch engine pointed at the given address (e.g.
// "https://opensearch:9200"). username/password may be empty for an unauthenticated cluster.
func NewOpenSearch(address, username, password string, logger *slog.Logger) (*OpenSearch, error) {
	client, err := opensearch.NewClient(opensearch.Config{
		Addresses: []string{address},
		Username:  username,
		Password:  password,
	})
	if err != nil {
		return nil, fmt.Errorf("store: opensearch client: %w", err)
	}
	return &OpenSearch{client: client, logger: logger, ensured: map[string]bool{}}, nil
}

// indexMappings defines the field mappings created on first use for each logical index. Project
// documents carry title/description/skills/budget/status; freelancer documents carry
// headline/bio/skills/rate. Unknown indices get a permissive default (no explicit mappings) so a
// new index kind still works without a code change.
var indexMappings = map[string]map[string]any{
	"projects": {
		"mappings": map[string]any{
			"properties": map[string]any{
				"title":            map[string]any{"type": "text"},
				"description":      map[string]any{"type": "text"},
				"skills":           map[string]any{"type": "keyword"},
				"budget_type":      map[string]any{"type": "keyword"},
				"budget_min_cents": map[string]any{"type": "long"},
				"budget_max_cents": map[string]any{"type": "long"},
				"status":           map[string]any{"type": "keyword"},
				"client_id":        map[string]any{"type": "keyword"},
				"published_at":     map[string]any{"type": "date"},
			},
		},
	},
	"freelancers": {
		"mappings": map[string]any{
			"properties": map[string]any{
				"headline":  map[string]any{"type": "text"},
				"bio":       map[string]any{"type": "text"},
				"skills":    map[string]any{"type": "keyword"},
				"rate":      map[string]any{"type": "long"},
				"rate_cents": map[string]any{"type": "long"},
				"user_id":   map[string]any{"type": "keyword"},
			},
		},
	},
}

// searchFields lists the analyzed text fields a multi_match query targets per index. Keeping it
// per-index lets projects and freelancers weight different fields.
var searchFields = map[string][]string{
	"projects":    {"title^3", "description", "skills"},
	"freelancers": {"headline^3", "bio", "skills"},
}

// ensureIndex creates the index with its mappings if it does not already exist. It is safe to
// call before every write: the existence check is cached per-index after the first success so we
// don't round-trip on the hot path.
func (e *OpenSearch) ensureIndex(ctx context.Context, index string) error {
	e.mu.Lock()
	if e.ensured[index] {
		e.mu.Unlock()
		return nil
	}
	e.mu.Unlock()

	existsRes, err := e.client.Indices.Exists([]string{index}, e.client.Indices.Exists.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("store: index exists check %q: %w", index, err)
	}
	defer existsRes.Body.Close()
	// Exists returns 200 when present, 404 when missing.
	if existsRes.StatusCode == 200 {
		e.markEnsured(index)
		return nil
	}

	var body io.Reader
	if m, ok := indexMappings[index]; ok {
		raw, mErr := json.Marshal(m)
		if mErr != nil {
			return fmt.Errorf("store: marshal mappings %q: %w", index, mErr)
		}
		body = bytes.NewReader(raw)
	}
	opts := []func(*opensearchapi.IndicesCreateRequest){e.client.Indices.Create.WithContext(ctx)}
	if body != nil {
		opts = append(opts, e.client.Indices.Create.WithBody(body))
	}
	createRes, err := e.client.Indices.Create(index, opts...)
	if err != nil {
		return fmt.Errorf("store: create index %q: %w", index, err)
	}
	defer createRes.Body.Close()
	// A concurrent creator (or a pre-existing index) yields 400 resource_already_exists_exception;
	// treat that as success rather than an error.
	if createRes.IsError() && createRes.StatusCode != 400 {
		return fmt.Errorf("store: create index %q: %s", index, createRes.String())
	}
	e.markEnsured(index)
	return nil
}

func (e *OpenSearch) markEnsured(index string) {
	e.mu.Lock()
	e.ensured[index] = true
	e.mu.Unlock()
}

// Index upserts a document into the given index keyed by id (a PUT with an explicit document id
// is an upsert in OpenSearch). The index is created with its mappings on first use. refresh=true
// makes the write immediately searchable, matching the at-least-once / idempotent expectations of
// the index consumer.
func (e *OpenSearch) Index(ctx context.Context, index, id string, doc map[string]any) error {
	if index == "" || id == "" {
		return nil
	}
	if err := e.ensureIndex(ctx, index); err != nil {
		return err
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("store: marshal doc: %w", err)
	}
	res, err := e.client.Index(
		index,
		bytes.NewReader(raw),
		e.client.Index.WithDocumentID(id),
		e.client.Index.WithContext(ctx),
		e.client.Index.WithRefresh("true"),
	)
	if err != nil {
		return fmt.Errorf("store: index %q/%s: %w", index, id, err)
	}
	defer res.Body.Close()
	if res.IsError() {
		return fmt.Errorf("store: index %q/%s: %s", index, id, res.String())
	}
	return nil
}

// Delete removes a document by id from the given index. A 404 (already gone) is treated as
// success so the operation is idempotent for the at-least-once consumer.
func (e *OpenSearch) Delete(ctx context.Context, index, id string) error {
	if index == "" || id == "" {
		return nil
	}
	res, err := e.client.Delete(index, id, e.client.Delete.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("store: delete %q/%s: %w", index, id, err)
	}
	defer res.Body.Close()
	if res.IsError() && res.StatusCode != 404 {
		return fmt.Errorf("store: delete %q/%s: %s", index, id, res.String())
	}
	return nil
}

// Search runs a bool query (multi_match over the index's text fields + filters) and returns the
// same Result shape the stub returns: a page of Hits (id, score, source) plus the total match
// count. Paging honors Query.Limit/Offset.
func (e *OpenSearch) Search(ctx context.Context, index string, q Query) (*Result, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 20
	}
	offset := q.Offset
	if offset < 0 {
		offset = 0
	}

	body := e.buildQuery(index, q, limit, offset)
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("store: marshal query: %w", err)
	}

	res, err := e.client.Search(
		e.client.Search.WithContext(ctx),
		e.client.Search.WithIndex(index),
		e.client.Search.WithBody(bytes.NewReader(raw)),
	)
	if err != nil {
		return nil, fmt.Errorf("store: search %q: %w", index, err)
	}
	defer res.Body.Close()
	if res.IsError() {
		// A query against a not-yet-created index returns 404; surface an empty page rather than
		// an error so a cold index behaves like the stub's empty result.
		if res.StatusCode == 404 {
			return &Result{Hits: []Hit{}, Total: 0}, nil
		}
		return nil, fmt.Errorf("store: search %q: %s", index, res.String())
	}

	return parseSearchResponse(res.Body)
}

// buildQuery assembles the OpenSearch query DSL: a multi_match over the index's text fields in
// the bool "must", plus structured filters in the bool "filter". An empty text falls back to
// match_all so filter-only searches still return documents.
func (e *OpenSearch) buildQuery(index string, q Query, limit, offset int) map[string]any {
	must := []any{}
	if q.Text != "" {
		fields := searchFields[index]
		if len(fields) == 0 {
			fields = []string{"*"}
		}
		must = append(must, map[string]any{
			"multi_match": map[string]any{
				"query":  q.Text,
				"fields": fields,
				"type":   "best_fields",
			},
		})
	} else {
		must = append(must, map[string]any{"match_all": map[string]any{}})
	}

	filter := buildFilters(q.Filters)

	return map[string]any{
		"from": offset,
		"size": limit,
		"query": map[string]any{
			"bool": map[string]any{
				"must":   must,
				"filter": filter,
			},
		},
	}
}

// buildFilters maps the engine-neutral Query.Filters to OpenSearch filter clauses. It interprets
// the same subset the stub understands (budget_type term, min/max budget ranges) plus a skills
// terms filter used by freelancer search.
func buildFilters(filters map[string]any) []any {
	out := []any{}
	if bt, ok := filters["budget_type"].(string); ok && bt != "" {
		out = append(out, map[string]any{"term": map[string]any{"budget_type": bt}})
	}
	if mn, ok := toInt64(filters["min_budget"]); ok && mn > 0 {
		out = append(out, map[string]any{"range": map[string]any{"budget_min_cents": map[string]any{"gte": mn}}})
	}
	if mx, ok := toInt64(filters["max_budget"]); ok && mx > 0 {
		out = append(out, map[string]any{"range": map[string]any{"budget_max_cents": map[string]any{"lte": mx}}})
	}
	if skills, ok := filters["skills"].([]string); ok && len(skills) > 0 {
		vals := make([]any, len(skills))
		for i, s := range skills {
			vals[i] = s
		}
		out = append(out, map[string]any{"terms": map[string]any{"skills": vals}})
	}
	return out
}

// toInt64 coerces the numeric filter values that may arrive as int64 (from the service layer) or
// float64 (if they were ever round-tripped through JSON).
func toInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case int:
		return int64(n), true
	case float64:
		return int64(n), true
	default:
		return 0, false
	}
}

// osSearchResponse is the minimal slice of the OpenSearch _search response we consume.
type osSearchResponse struct {
	Hits struct {
		Total struct {
			Value int `json:"value"`
		} `json:"total"`
		Hits []struct {
			ID     string         `json:"_id"`
			Score  float64        `json:"_score"`
			Source map[string]any `json:"_source"`
		} `json:"hits"`
	} `json:"hits"`
}

// parseSearchResponse decodes the OpenSearch response body into our Result shape.
func parseSearchResponse(body io.Reader) (*Result, error) {
	var parsed osSearchResponse
	if err := json.NewDecoder(body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("store: decode search response: %w", err)
	}
	hits := make([]Hit, 0, len(parsed.Hits.Hits))
	for _, h := range parsed.Hits.Hits {
		src := h.Source
		if src == nil {
			src = map[string]any{}
		}
		hits = append(hits, Hit{ID: h.ID, Score: h.Score, Source: src})
	}
	return &Result{Hits: hits, Total: parsed.Hits.Total.Value}, nil
}

// NewSearchEngine selects the SearchEngine at runtime: when esURL is non-empty it returns a real
// *OpenSearch engine (and logs the choice); otherwise it returns the Postgres-FTS *OpenSearchStub.
// pool is always required for the stub's full-text fallback and for suggestions. If the real
// client fails to construct, it logs and falls back to the stub so the service still boots.
func NewSearchEngine(pool *pgxpool.Pool, esURL, username, password string, logger *slog.Logger) SearchEngine {
	if esURL == "" {
		return NewOpenSearchStub(pool, esURL, logger)
	}
	engine, err := NewOpenSearch(esURL, username, password, logger)
	if err != nil {
		if logger != nil {
			logger.Error("opensearch: client init failed, falling back to stub", "err", err)
		}
		return NewOpenSearchStub(pool, esURL, logger)
	}
	if logger != nil {
		logger.Info("opensearch: using live client", "address", esURL)
	}
	return engine
}
