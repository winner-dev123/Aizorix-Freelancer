// Package httpapi is the REST transport for the search service. Search is public: no
// identity headers are required, though the gateway may still inject X-User-Id /
// X-User-Roles / X-Account-Type for logging.
package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/aizorix/platform/search/internal/service"
	"github.com/aizorix/platform/search/internal/store"
	"github.com/go-chi/chi/v5"
)

type API struct {
	svc    *service.Service
	logger *slog.Logger
}

func New(svc *service.Service, logger *slog.Logger) *API { return &API{svc: svc, logger: logger} }

func (a *API) Routes() http.Handler {
	r := chi.NewRouter()
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	r.Handle("/metrics", a.metrics())
	r.Route("/v1/search", func(r chi.Router) {
		r.Get("/projects", a.searchProjects)
		r.Get("/freelancers", a.searchFreelancers)
		r.Get("/suggest", a.suggest)
	})
	return r
}

func (a *API) searchProjects(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := pageParams(q)
	res, err := a.svc.SearchProjects(r.Context(), q.Get("q"), service.ProjectFilters{
		BudgetType: q.Get("budget_type"),
		MinBudget:  parseCents(q.Get("min")),
		MaxBudget:  parseCents(q.Get("max")),
	}, limit, offset)
	if err != nil {
		a.mapError(w, err)
		return
	}
	writeResult(w, res)
}

func (a *API) searchFreelancers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := pageParams(q)
	var skills []string
	if raw := q.Get("skills"); raw != "" {
		for _, s := range strings.Split(raw, ",") {
			if s = strings.TrimSpace(s); s != "" {
				skills = append(skills, s)
			}
		}
	}
	res, err := a.svc.SearchFreelancers(r.Context(), q.Get("q"), skills, limit, offset)
	if err != nil {
		a.mapError(w, err)
		return
	}
	writeResult(w, res)
}

func (a *API) suggest(w http.ResponseWriter, r *http.Request) {
	suggestions, err := a.svc.Suggest(r.Context(), r.URL.Query().Get("q"))
	if err != nil {
		a.mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"suggestions": suggestions})
}

func writeResult(w http.ResponseWriter, res *store.Result) {
	hits := res.Hits
	if hits == nil {
		hits = []store.Hit{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"hits":  hits,
		"total": res.Total,
	})
}

// pageParams reads limit/offset with sane defaults and caps.
func pageParams(q map[string][]string) (limit, offset int) {
	limit = atoiDefault(first(q, "limit"), 20)
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	offset = atoiDefault(first(q, "offset"), 0)
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

func first(q map[string][]string, key string) string {
	if v, ok := q[key]; ok && len(v) > 0 {
		return v[0]
	}
	return ""
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

// parseCents parses an integer cents filter; empty or invalid returns 0 (no filter).
func parseCents(s string) int64 {
	if s == "" {
		return 0
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func (a *API) metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte("# HELP search_up\n# TYPE search_up gauge\nsearch_up 1\n"))
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"code": code, "message": msg})
}

// mapError translates domain/store errors into HTTP responses.
func (a *API) mapError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "resource not found")
	default:
		a.logger.Error("request failed", "err", err)
		writeErr(w, http.StatusInternalServerError, "INTERNAL", "internal error")
	}
}
