package main

import (
	"context"
	"net/http"
	"sort"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/tracker"
	"github.com/titaniumcoder/pocket-cfo/internal/render"
	"github.com/titaniumcoder/pocket-cfo/internal/webui"
)

type infoWorkspaceView struct {
	ID      int
	Name    string
	Clients []tracker.Client
}

type infoCountryView struct {
	IsoCode      string
	Name         string
	Subdivisions []tracker.Subdivision
}

type infoView struct {
	Header webui.Header

	TogglConfigured bool
	TogglErr        string
	Workspaces      []infoWorkspaceView

	HolidaysErr string
	Countries   []infoCountryView

	API2PDFConfigured bool
	API2PDFErr        string
	Balance           render.BalanceInfo
}

// handleInfo serves the admin-only /info diagnostics page: whichever
// external services are configured, showing IDs/reference values useful for
// filling in a recipient's tracking_client_id, config.json's
// holidayCountry/holidaySubdivision, and the api2pdf account status. Gated
// stricter than the finance/invoicing parts (s.authorized only, no
// email-OTP readOnly tier) since this can expose account/billing data.
func (s *server) handleInfo(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.currentSession(r)
	if !ok || !s.authorized(sess) {
		http.Error(w, "you don't have access to this page", http.StatusForbidden)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	view := infoView{Header: s.header(sess, webui.PageInfo)}

	if s.tracker.Toggl != nil {
		view.TogglConfigured = true
		view.Workspaces, view.TogglErr = loadTogglInfo(ctx, s.tracker.Toggl)
	}

	view.Countries, view.HolidaysErr = loadHolidayInfo(ctx, s.tracker.Holidays)

	if s.cfg.api2pdfKey != "" {
		view.API2PDFConfigured = true
		balance, err := render.NewAPI2PDF(s.cfg.api2pdfKey).Balance(ctx)
		if err != nil {
			view.API2PDFErr = err.Error()
		} else {
			view.Balance = balance
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.infoTmpl.Execute(w, view); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// loadTogglInfo lists every workspace the token can see and, nested under
// each, its clients — specifically to help pick the right
// tracking_client_id value per recipient (see schemas/recipient.json).
func loadTogglInfo(ctx context.Context, tg *tracker.Toggl) ([]infoWorkspaceView, string) {
	workspaces, err := tg.Workspaces(ctx)
	if err != nil {
		return nil, err.Error()
	}
	out := make([]infoWorkspaceView, 0, len(workspaces))
	for _, w := range workspaces {
		clients, err := tg.Clients(ctx, w.ID)
		if err != nil {
			return nil, err.Error()
		}
		sort.Slice(clients, func(i, j int) bool { return clients[i].Name < clients[j].Name })
		out = append(out, infoWorkspaceView{ID: w.ID, Name: w.Name, Clients: clients})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, ""
}

// loadHolidayInfo lists every country OpenHolidays knows about and, nested
// under each, its subdivisions — a reference list for picking config.json's
// holidayCountry/holidaySubdivision. One country's subdivision-fetch
// failure only blanks that country's own list (best-effort), rather than
// failing the whole reference page.
func loadHolidayInfo(ctx context.Context, h *tracker.Holidays) ([]infoCountryView, string) {
	countries, err := h.Countries(ctx)
	if err != nil {
		return nil, err.Error()
	}
	out := make([]infoCountryView, 0, len(countries))
	for _, c := range countries {
		subs, err := h.Subdivisions(ctx, c.IsoCode)
		if err != nil {
			subs = nil
		}
		out = append(out, infoCountryView{IsoCode: c.IsoCode, Name: c.Name, Subdivisions: subs})
	}
	return out, ""
}
