package main

import (
	"context"
	"net/http"
	"sort"
	"time"

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

type infoTogglPanel struct {
	Configured bool
	Active     bool
	Err        string
	KeyNote    string
	KeyExpired bool
	Workspaces []infoWorkspaceView
}

type infoView struct {
	Header webui.Header

	ConfigGroups []configGroup

	TogglMode tracker.Mode
	Track     infoTogglPanel
	Focus     infoTogglPanel

	Rules []tracker.RuleChange

	HolidaysErr string
	Countries   []infoCountryView

	API2PDFConfigured bool
	API2PDFErr        string
	Balance           render.BalanceInfo
}

func (s *server) handleInfo(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.currentSession(r)
	if !ok || !s.authenticated(sess) {
		s.rememberDestination(w, r)
		http.Redirect(w, r, "/auth/login", http.StatusFound)
		return
	}
	if !s.authorized(sess) {
		http.Error(w, "you don't have access to this page", http.StatusForbidden)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	view := infoView{
		Header:       s.header(sess, webui.PageInfo, webui.ParsePeriod(r.URL.Query().Get("year"), r.URL.Query().Get("month"))),
		ConfigGroups: s.configGroups(),
	}
	view.ConfigGroups = append(view.ConfigGroups, configGroup{
		Name: "This session",
		Rows: []configRow{
			{Name: "Login", Value: orUnset(sess.Login)},
			{Name: "Permission", Value: orUnset(sess.Permission)},
			{Name: "Avatar URL", Value: orUnset(view.Header.AvatarURL())},
		},
	})

	view.Rules = tracker.RulesTimeline(s.tracker.Personal, s.tracker.Start, time.Now())

	mode := s.cfg.togglMode
	view.TogglMode = togglModeLabel(mode)
	view.Track = togglPanel(ctx, s.togglTrack, mode == togglModeTrack || mode == togglModeBoth)
	view.Focus = togglPanel(ctx, s.togglFocus, mode == togglModeFocus || mode == togglModeBoth)

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
		serverError(w, r, "loading data", err)
	}
}

func togglPanel(ctx context.Context, tg *tracker.Toggl, active bool) infoTogglPanel {
	if tg == nil {
		return infoTogglPanel{}
	}
	panel := infoTogglPanel{Configured: true, Active: active}
	panel.Workspaces, panel.Err = loadTogglInfo(ctx, tg)
	ks := tg.KeyStatus(time.Now())
	panel.KeyNote, panel.KeyExpired = ks.Warning, ks.Expired
	return panel
}

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
