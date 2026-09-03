package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/tracker"
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
	KeyOnly    bool
	Active     bool
	Err        string
	KeyNote    string
	KeyExpired bool
	Workspaces []infoWorkspaceView
}

type infoView struct {
	Header webui.Header

	ConfigGroups []configGroup

	TogglMode  tracker.Mode
	Track      infoTogglPanel
	Focus      infoTogglPanel
	CacheStats []configGroup

	Rules []tracker.RuleChange

	HolidaysErr string
	Countries   []infoCountryView
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
	if s.togglFocus != nil && !toggl2Complete(s.cfg.finance) {
		view.Focus = infoTogglPanel{Configured: true, KeyOnly: true, KeyNote: view.Focus.KeyNote, KeyExpired: view.Focus.KeyExpired}
	}

	view.CacheStats = cacheStatsGroups(s.togglTrack, s.togglFocus, time.Now())
	view.Countries, view.HolidaysErr = loadHolidayInfo(ctx, s.tracker.Holidays)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.infoTmpl.Execute(w, view); err != nil {
		serverError(w, r, "loading data", err)
	}
}

func (s *server) handleTogglReset(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.currentSession(r)
	if !ok || !s.authenticated(sess) {
		http.Redirect(w, r, "/auth/login", http.StatusFound)
		return
	}
	if !s.authorized(sess) {
		http.Error(w, "you don't have access to this page", http.StatusForbidden)
		return
	}
	s.togglTrack.Reset()
	s.togglFocus.Reset()
	log.Printf("toggl: cache reset by %s", sess.Login)
	http.Redirect(w, r, "/info", http.StatusSeeOther)
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

func cacheStatsGroups(track, focus *tracker.Toggl, now time.Time) []configGroup {
	var groups []configGroup
	for _, side := range []struct {
		name string
		tg   *tracker.Toggl
	}{{"Toggl Track", track}, {"Toggl 2.0", focus}} {
		if side.tg == nil {
			continue
		}
		groups = append(groups, configGroup{Name: side.name, Rows: cacheStatsRows(side.tg.Stats(now), side.tg.Quota(now), now)})
	}
	return groups
}

func cacheStatsRows(s tracker.CacheStats, q tracker.QuotaStatus, now time.Time) []configRow {
	at := func(t time.Time) string {
		if t.IsZero() {
			return "—"
		}
		return t.In(now.Location()).Format("02 Jan 2006 15:04:05")
	}
	rows := []configRow{
		{Name: "Months cached", Value: fmt.Sprintf("%d (%d stale, waiting for a refresh)", s.Months, s.StaleMonths)},
		{Name: "Months fetched between", Value: at(s.Oldest) + " and " + at(s.Newest)},
		{Name: "Entries in memory", Value: fmt.Sprintf("%d (months, projects, rates, workspaces, clients)", s.Entries)},
		{Name: "Served from cache", Value: fmt.Sprintf("%d fresh, %d stale while Toggl could not be asked", s.Hits, s.StaleServed)},
		{Name: "Fetches", Value: fmt.Sprintf("%d, %d of them failed", s.Fetches, s.Failures)},
		{Name: "HTTP requests to Toggl", Value: fmt.Sprintf("%d, %d of them retries", s.Requests, s.Retries)},
		{Name: "Last fetch", Value: lastFetch(s, at)},
		{Name: "In flight now", Value: fmt.Sprintf("%d fetches, %d breakers open", s.InFlight, s.OpenBreakers)},
	}
	if q.Exhausted || q.Remaining >= 0 {
		rows = append(rows, configRow{Name: "Hourly quota", Value: strings.TrimPrefix(describeQuota(q, now), "Hourly request quota: ")})
	}
	switch {
	case s.SnapshotPath == "":
		rows = append(rows, configRow{Name: "Snapshot", Value: "none — memory only (TOGGL_CACHE_DIR unset)"})
	case s.SnapshotBytes == 0:
		rows = append(rows, configRow{Name: "Snapshot", Value: s.SnapshotPath + " — not written yet"})
	default:
		rows = append(rows, configRow{Name: "Snapshot", Value: fmt.Sprintf("%s — %d bytes, written %s", s.SnapshotPath, s.SnapshotBytes, at(s.SnapshotWrittenAt))})
	}
	return rows
}

func lastFetch(s tracker.CacheStats, at func(time.Time) string) string {
	if s.LastFetchAt.IsZero() {
		return "none since this process started"
	}
	return fmt.Sprintf("%s, took %s", at(s.LastFetchAt), s.LastFetchTook.Round(time.Millisecond))
}

func describeQuota(q tracker.QuotaStatus, now time.Time) string {
	switch {
	case q.Exhausted:
		return q.Note
	case q.Remaining < 0:
		return "Hourly request quota: not reported by this API."
	}
	return fmt.Sprintf("Hourly request quota: %d requests left, window resets at %s.", q.Remaining, q.ResetAt.In(now.Location()).Format("15:04"))
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
