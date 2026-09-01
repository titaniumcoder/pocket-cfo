package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/api"
	"github.com/titaniumcoder/pocket-cfo/internal/finance/tracker"
)

const apiRequestTimeout = 30 * time.Second

const mcpServerVersion = "4"

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
	mux.HandleFunc("GET /api/config", s.apiFinanceConfig)
	mux.HandleFunc("GET /api/director-loan/{month}", s.apiDirectorLoan)
	mux.HandleFunc("GET /api/invoices", s.apiInvoices)
	mux.HandleFunc("GET /api/invoices/{number}/document", s.apiInvoiceDocument)
	mux.HandleFunc("GET /api/invoices/{number}/pdf", s.apiInvoicePDF)
	mux.HandleFunc("POST /api/invoices/paid", s.apiSetInvoicePaid)
	mux.HandleFunc("POST /api/invoices/draft", s.apiSaveInvoiceDraft)
	mux.HandleFunc("POST /api/invoices/issue", s.apiIssueInvoice)
	mux.HandleFunc("POST /api/actuals/add", s.apiAddActuals)
	mux.HandleFunc("POST /api/actuals/edit", s.apiEditActuals)
	mux.HandleFunc("POST /api/accounts/balance", s.apiRecordAccountBalance)
	mux.HandleFunc("POST /api/budget/move-planned-expense", s.apiMovePlannedExpense)
	mux.HandleFunc("POST /api/budget/schedule-amount-change", s.apiScheduleAmountChange)

	mcpHandler := s.requireBearer(capBody(maxMCPBody, s.apiService().MCPHandler(mcpServerVersion)))
	mux.Handle("POST /mcp", mcpHandler)
	mux.Handle("GET /mcp", mcpHandler)
	mux.Handle("DELETE /mcp", mcpHandler)
	mux.HandleFunc("GET /api/{a}", s.apiNotFound)
	mux.HandleFunc("GET /api/{a}/{b}", s.apiNotFound)
}

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

func (s *server) requireBearer(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.apiAuthorized(w, r) {
			return
		}
		h.ServeHTTP(w, r)
	})
}

func (s *server) apiService() *api.Service {
	svc := &api.Service{}
	if s.tracker != nil {
		svc.Budget, svc.Accounts, svc.Actuals = s.tracker.Budget, s.tracker.Accounts, s.tracker.Actuals
		svc.Start = s.tracker.Start
		svc.Personal = s.tracker.Personal
		svc.InvoicesDir = invoicesDir
		svc.PaidInvoicesPath = paidInvoicesPath
		svc.CatalogPath = catalogNotesPath()
		// The same computation the dashboard renders, invoices and all, so a
		// figure this API reports and a figure on the page cannot drift.
		svc.Figures = func(ctx context.Context, year int, month time.Month) (tracker.Figures, error) {
			trk, err := s.trackerForRequest()
			if err != nil {
				return tracker.Figures{}, err
			}
			return trk.ComputeMonth(ctx, year, month), nil
		}
	}
	if s.cfg.githubDataToken != "" {
		svc.Store = &api.ContentsClient{
			HTTP:    s.httpClient,
			Repo:    s.cfg.repo,
			Token:   s.cfg.githubDataToken,
			BaseURL: s.cfg.githubAPIURL,
		}
	}
	return svc
}

const maxPutBody = 1 << 20

const maxMCPBody = 2 << 20

func capBody(limit int64, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		h.ServeHTTP(w, r)
	})
}

func decodeAPIBody(w http.ResponseWriter, r *http.Request, into any) bool {
	if ct := r.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, "application/json") {
		writeAPIError(w, &api.Error{Code: api.CodeInvalidRequest, Message: "Content-Type must be application/json"}, http.StatusUnsupportedMediaType)
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPutBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeAPIError(w, &api.Error{Code: api.CodeInvalidRequest, Message: "body too large"}, http.StatusRequestEntityTooLarge)
			return false
		}
		writeAPIError(w, &api.Error{Code: api.CodeInvalidRequest, Message: err.Error()}, http.StatusBadRequest)
		return false
	}
	if dec.More() {
		writeAPIError(w, &api.Error{Code: api.CodeInvalidRequest, Message: "the body must be one JSON object"}, http.StatusBadRequest)
		return false
	}
	return true
}

func (s *server) apiAddActuals(w http.ResponseWriter, r *http.Request) {
	if !s.apiAuthorized(w, r) {
		return
	}
	var req api.AddRequest
	if !decodeAPIBody(w, r, &req) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	out, err := s.apiService().AddTransactions(ctx, req)
	if err != nil {
		writeAPIError(w, err, apiStatus(err))
		return
	}
	writeAPIJSON(w, http.StatusOK, out)
}

func (s *server) apiEditActuals(w http.ResponseWriter, r *http.Request) {
	if !s.apiAuthorized(w, r) {
		return
	}
	var req api.EditRequest
	if !decodeAPIBody(w, r, &req) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	out, err := s.apiService().EditTransactions(ctx, req)
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

func (s *server) apiFinanceConfig(w http.ResponseWriter, r *http.Request) {
	s.serveAPI(w, r, func(ctx context.Context, svc *api.Service) (any, error) {
		return svc.FinanceConfig(ctx)
	})
}

func (s *server) apiDirectorLoan(w http.ResponseWriter, r *http.Request) {
	month := r.PathValue("month")
	s.serveAPI(w, r, func(ctx context.Context, svc *api.Service) (any, error) {
		return svc.DirectorLoanFor(ctx, month)
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
	years, err := decodeYears(q["year"])
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
		OnlyUntracked:  q.Get("only_untracked") == "true",
		OnlyMovements:  q.Get("only_movements") == "true",
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
		year, err := strconv.Atoi(raw)
		if raw == "" {
			year = time.Now().Year()
		} else if err != nil {
			return nil, &api.Error{Code: api.CodeInvalidRequest, Message: "year must look like 2026"}
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

func decodeYears(explicit []string) ([]int, error) {
	var out []int
	for _, raw := range explicit {
		y, err := strconv.Atoi(raw)
		if err != nil || y < 1970 || y > 9999 {
			return nil, &api.Error{Code: api.CodeInvalidRequest, Message: "year must look like 2026"}
		}
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
	case api.CodeInvalidRequest, api.CodeValidationFailed:
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

func (s *server) apiInvoices(w http.ResponseWriter, r *http.Request) {
	year := r.URL.Query().Get("year")
	s.serveAPI(w, r, func(ctx context.Context, svc *api.Service) (any, error) {
		return svc.Invoices(ctx, year)
	})
}

func (s *server) apiSetInvoicePaid(w http.ResponseWriter, r *http.Request) {
	if !s.apiAuthorized(w, r) {
		return
	}
	var req api.InvoicePaymentRequest
	if !decodeAPIBody(w, r, &req) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	out, err := s.apiService().SetInvoicePaid(ctx, req)
	if err != nil {
		writeAPIError(w, err, apiStatus(err))
		return
	}
	writeAPIJSON(w, http.StatusOK, out)
}

func (s *server) apiInvoiceDocument(w http.ResponseWriter, r *http.Request) {
	number := r.PathValue("number")
	s.serveAPI(w, r, func(ctx context.Context, svc *api.Service) (any, error) {
		return svc.InvoiceDocumentFor(ctx, number)
	})
}

func (s *server) apiInvoicePDF(w http.ResponseWriter, r *http.Request) {
	if !s.apiAuthorized(w, r) {
		return
	}
	number := r.PathValue("number")
	variant := r.URL.Query().Get("variant")

	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	target, err := s.apiService().InvoicePDFTarget(ctx, number, variant)
	if err != nil {
		writeAPIError(w, err, apiStatus(err))
		return
	}
	path := filepath.Join(buildDir, target.Filename)
	f, err := os.Open(path)
	if err != nil {
		writeAPIError(w, &api.Error{
			Code:    api.CodeNotFound,
			Message: target.Filename + " is not in the build output yet — the CI build that renders PDFs runs on the data repo after the commit deploys",
		}, http.StatusNotFound)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", target.Filename))
	http.ServeContent(w, r, target.Filename, time.Time{}, f)
}

func (s *server) apiSaveInvoiceDraft(w http.ResponseWriter, r *http.Request) {
	if !s.apiAuthorized(w, r) {
		return
	}
	var req api.SaveDraftRequest
	if !decodeAPIBody(w, r, &req) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	out, err := s.apiService().SaveDraftInvoice(ctx, req)
	if err != nil {
		writeAPIError(w, err, apiStatus(err))
		return
	}
	writeAPIJSON(w, http.StatusOK, out)
}

func (s *server) apiIssueInvoice(w http.ResponseWriter, r *http.Request) {
	if !s.apiAuthorized(w, r) {
		return
	}
	var req api.IssueInvoiceRequest
	if !decodeAPIBody(w, r, &req) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	out, err := s.apiService().IssueInvoice(ctx, req)
	if err != nil {
		writeAPIError(w, err, apiStatus(err))
		return
	}
	writeAPIJSON(w, http.StatusOK, out)
}

func (s *server) apiRecordAccountBalance(w http.ResponseWriter, r *http.Request) {
	if !s.apiAuthorized(w, r) {
		return
	}
	var req api.RecordBalanceRequest
	if !decodeAPIBody(w, r, &req) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	out, err := s.apiService().RecordAccountBalance(ctx, req)
	if err != nil {
		writeAPIError(w, err, apiStatus(err))
		return
	}
	writeAPIJSON(w, http.StatusOK, out)
}

func (s *server) apiMovePlannedExpense(w http.ResponseWriter, r *http.Request) {
	if !s.apiAuthorized(w, r) {
		return
	}
	var req api.MoveRequest
	if !decodeAPIBody(w, r, &req) {
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

func (s *server) apiScheduleAmountChange(w http.ResponseWriter, r *http.Request) {
	if !s.apiAuthorized(w, r) {
		return
	}
	var req api.ScheduleAmountChangeRequest
	if !decodeAPIBody(w, r, &req) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	out, err := s.apiService().ScheduleAmountChange(ctx, req)
	if err != nil {
		writeAPIError(w, err, apiStatus(err))
		return
	}
	writeAPIJSON(w, http.StatusOK, out)
}
