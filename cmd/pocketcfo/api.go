package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/api"
)

// apiRequestTimeout is separate from the page timeout: these are local file
// reads, so anything slow is a bug rather than a slow upstream.
const apiRequestTimeout = 30 * time.Second

// registerAPI adds the Hermes routes, and only when a token is configured —
// unconfigured, the paths do not exist at all rather than 401.
func (s *server) registerAPI(mux *http.ServeMux) {
	if s.cfg.hermesAPIToken == "" {
		return
	}
	mux.HandleFunc("GET /api/budget/categories", s.apiBudgetCategories)
	mux.HandleFunc("GET /api/budget/{period}", s.apiBudgetPeriod)
	mux.HandleFunc("GET /api/accounts", s.apiAccounts)
	mux.HandleFunc("GET /api/actuals/{month}", s.apiActuals)
	mux.HandleFunc("GET /api/transactions", s.apiTransactions)
	mux.HandleFunc("GET /api/reconciliation", s.apiReconciliation)
	mux.HandleFunc("PUT /api/actuals/{month}", s.apiPutActuals)
	mux.HandleFunc("POST /api/budget/move-planned-expense", s.apiMovePlannedExpense)
	// So a typo'd path answers Hermes in JSON rather than with a finance page.
	// Two exact shapes rather than an /api/ subtree: a subtree overlaps
	// /{year}/{month} with neither more specific, which ServeMux rejects.
	// Behind the same gate, so they leak nothing either.
	mux.HandleFunc("GET /api/{a}", s.apiNotFound)
	mux.HandleFunc("GET /api/{a}/{b}", s.apiNotFound)
}

// apiAuthorized checks the bearer token. It never consults currentSession:
// outside prod that hands admin to every local request, and the API must not
// inherit it.
func (s *server) apiAuthorized(w http.ResponseWriter, r *http.Request) bool {
	const prefix = "Bearer "
	header := r.Header.Get("Authorization")
	token := ""
	if strings.HasPrefix(header, prefix) {
		token = header[len(prefix):]
	}
	want := s.cfg.hermesAPIToken
	if want == "" || subtle.ConstantTimeCompare([]byte(token), []byte(want)) != 1 {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeAPIError(w, &api.Error{Code: "unauthorized", Message: "a valid bearer token is required"}, http.StatusUnauthorized)
		return false
	}
	return true
}

func (s *server) apiService() *api.Service {
	svc := &api.Service{Budget: s.tracker.Budget, Accounts: s.tracker.Accounts, Actuals: s.tracker.Actuals}
	if s.cfg.githubDataToken != "" {
		svc.Store = &api.ContentsClient{
			HTTP:    s.httpClient, // GitHub is fast-fail territory, unlike Toggl
			Repo:    s.cfg.repo,
			Token:   s.cfg.githubDataToken,
			BaseURL: s.cfg.githubAPIURL,
		}
	}
	return svc
}

// maxPutBody caps a month document. A year of statements is a few hundred KB;
// anything larger is a mistake rather than a reconciliation.
const maxPutBody = 1 << 20

func (s *server) apiPutActuals(w http.ResponseWriter, r *http.Request) {
	if !s.apiAuthorized(w, r) {
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, "application/json") {
		writeAPIError(w, &api.Error{Code: api.CodeInvalidRequest, Message: "Content-Type must be application/json"}, http.StatusUnsupportedMediaType)
		return
	}

	var req api.PutRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPutBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeAPIError(w, &api.Error{Code: api.CodeInvalidRequest, Message: "body too large"}, http.StatusRequestEntityTooLarge)
			return
		}
		writeAPIError(w, &api.Error{Code: api.CodeInvalidRequest, Message: err.Error()}, http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	out, err := s.apiService().PutActuals(ctx, r.PathValue("month"), req)
	if err != nil {
		writeAPIError(w, err, apiStatus(err))
		return
	}
	writeAPIJSON(w, http.StatusOK, out)
}

func (s *server) apiBudgetCategories(w http.ResponseWriter, r *http.Request) {
	s.serveAPI(w, r, func(ctx context.Context, svc *api.Service) (any, error) {
		return svc.Categories(ctx)
	})
}

func (s *server) apiBudgetPeriod(w http.ResponseWriter, r *http.Request) {
	period := r.PathValue("period")
	s.serveAPI(w, r, func(ctx context.Context, svc *api.Service) (any, error) {
		if len(period) == 4 {
			return svc.BudgetForYear(ctx, period)
		}
		return svc.BudgetForMonth(ctx, period)
	})
}

func (s *server) apiAccounts(w http.ResponseWriter, r *http.Request) {
	s.serveAPI(w, r, func(ctx context.Context, svc *api.Service) (any, error) {
		return svc.AccountsList(ctx)
	})
}

func (s *server) apiActuals(w http.ResponseWriter, r *http.Request) {
	month := r.PathValue("month")
	s.serveAPI(w, r, func(ctx context.Context, svc *api.Service) (any, error) {
		return svc.ActualsFor(ctx, month)
	})
}

func (s *server) apiTransactions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	years, err := parseYears(q["year"], q.Get("from"), q.Get("to"))
	if err != nil {
		if !s.apiAuthorized(w, r) {
			return
		}
		writeAPIError(w, err, http.StatusBadRequest)
		return
	}
	sq := api.SearchQuery{
		Query:          q.Get("q"),
		From:           q.Get("from"),
		To:             q.Get("to"),
		Category:       q.Get("category"),
		Account:        q.Get("account"),
		IncludeIgnored: q.Get("include_ignored") == "true",
		Limit:          limit,
		Years:          years,
	}
	s.serveAPI(w, r, func(ctx context.Context, svc *api.Service) (any, error) {
		return svc.Search(ctx, sq)
	})
}

func (s *server) apiReconciliation(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("year")
	s.serveAPI(w, r, func(ctx context.Context, svc *api.Service) (any, error) {
		year := time.Now().Year()
		if raw != "" {
			y, err := strconv.Atoi(raw)
			if err != nil || y < 1970 || y > 9999 {
				return nil, &api.Error{Code: api.CodeInvalidRequest, Message: "year must look like 2026"}
			}
			year = y
		}
		return svc.Reconciliation(ctx, year)
	})
}

func (s *server) apiNotFound(w http.ResponseWriter, r *http.Request) {
	if !s.apiAuthorized(w, r) {
		return
	}
	writeAPIError(w, &api.Error{Code: api.CodeNotFound, Message: "no such endpoint"}, http.StatusNotFound)
}

// serveAPI is the whole adapter: authenticate, call one service method,
// encode. Nothing here computes.
func (s *server) serveAPI(w http.ResponseWriter, r *http.Request, fn func(context.Context, *api.Service) (any, error)) {
	if !s.apiAuthorized(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	out, err := fn(ctx, s.apiService())
	if err != nil {
		writeAPIError(w, err, apiStatus(err))
		return
	}
	writeAPIJSON(w, http.StatusOK, out)
}

// parseYears works out which years a search should scan: the explicit ?year=
// values, else the span from/to covers, else the current year.
func parseYears(explicit []string, from, to string) ([]int, error) {
	seen := map[int]bool{}
	add := func(y int) {
		if y >= 1970 && y <= 9999 {
			seen[y] = true
		}
	}
	for _, raw := range explicit {
		y, err := strconv.Atoi(raw)
		if err != nil || y < 1970 || y > 9999 {
			return nil, &api.Error{Code: api.CodeInvalidRequest, Message: "year must look like 2026"}
		}
		add(y)
	}
	for _, bound := range []string{from, to} {
		if len(bound) >= 4 {
			if y, err := strconv.Atoi(bound[:4]); err == nil {
				add(y)
			}
		}
	}
	if len(seen) == 0 {
		return nil, nil
	}
	// Fill the gap, so from=2024-01&to=2026-12 scans 2025 too.
	lo, hi := 9999, 0
	for y := range seen {
		if y < lo {
			lo = y
		}
		if y > hi {
			hi = y
		}
	}
	var out []int
	for y := lo; y <= hi; y++ {
		out = append(out, y)
	}
	return out, nil
}

func apiStatus(err error) int {
	e, ok := err.(*api.Error)
	if !ok {
		return http.StatusInternalServerError
	}
	switch e.Code {
	case api.CodeInvalidRequest, api.CodeValidationFailed, api.CodeWouldRemove:
		return http.StatusBadRequest
	case api.CodeNotFound:
		return http.StatusNotFound
	case api.CodeConflict:
		return http.StatusConflict
	case api.CodeWriteNotConfigured:
		return http.StatusServiceUnavailable
	case api.CodeUpstream:
		return http.StatusBadGateway
	}
	return http.StatusInternalServerError
}

type apiErrorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Details any    `json:"details,omitempty"`
	} `json:"error"`
}

func writeAPIError(w http.ResponseWriter, err error, status int) {
	var body apiErrorBody
	if e, ok := err.(*api.Error); ok {
		body.Error.Code, body.Error.Message, body.Error.Details = e.Code, e.Message, e.Details
	} else {
		body.Error.Code, body.Error.Message = api.CodeInternal, "internal error"
	}
	writeAPIJSON(w, status, body)
}

func writeAPIJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// apiMovePlannedExpense shifts one dated category to the month it was actually
// charged in. Narrow on purpose: budget.json drives every figure on the
// dashboard, so this changes a date and nothing else.
func (s *server) apiMovePlannedExpense(w http.ResponseWriter, r *http.Request) {
	if !s.apiAuthorized(w, r) {
		return
	}
	var req api.MoveRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPutBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeAPIError(w, &api.Error{Code: api.CodeInvalidRequest, Message: err.Error()}, http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	out, err := s.apiService().MovePlannedExpense(ctx, req)
	if err != nil {
		writeAPIError(w, err, apiStatus(err))
		return
	}
	writeAPIJSON(w, http.StatusOK, out)
}
