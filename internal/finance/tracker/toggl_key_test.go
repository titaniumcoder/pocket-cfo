package tracker

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func fetchStatus(code int) func(context.Context) (any, error) {
	return func(context.Context) (any, error) {
		return nil, apiError("toggl", statusResponse(code, "nope"))
	}
}

func fetchOK(context.Context) (any, error) { return "fine", nil }

func TestUnauthorizedIsRememberedUntilAFetchSucceeds(t *testing.T) {
	tg := &Toggl{}
	ctx := context.Background()
	today := time.Now()

	if _, err := tg.getCached(ctx, "k", time.Time{}, time.Time{}, fetchStatus(http.StatusUnauthorized)); err == nil {
		t.Fatal("expected the 401 to surface as an error")
	}
	s := tg.KeyStatus(today)
	if !s.Rejected || !s.Expired || s.RejectedAt.IsZero() {
		t.Fatalf("after a 401, KeyStatus = %+v, want Rejected and Expired", s)
	}
	if !strings.Contains(s.Warning, "HTTP 401") || !strings.Contains(s.Warning, "TOGGL_API_TOKEN") {
		t.Errorf("warning = %q, want it to name the 401 and the env var", s.Warning)
	}

	tg.EvictRange(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC))
	if !tg.KeyStatus(today).Rejected {
		t.Error("Reload (EvictRange) must not forget a rejected key")
	}

	if _, err := tg.getCached(ctx, "other", time.Time{}, time.Time{}, fetchStatus(http.StatusNotFound)); err == nil {
		t.Fatal("expected the 404 to surface as an error")
	}
	if !tg.KeyStatus(today).Rejected {
		t.Error("an unrelated failure must not clear the rejection")
	}

	if _, err := tg.getCached(ctx, "k", time.Time{}, time.Time{}, fetchOK); err != nil {
		t.Fatal(err)
	}
	if s := tg.KeyStatus(today); s.Rejected || s.Warning != "" {
		t.Errorf("after a success, KeyStatus = %+v, want clean", s)
	}
}

func TestKeyWarningCountsDownToTheExpiryDate(t *testing.T) {
	today := time.Date(2026, 9, 3, 15, 0, 0, 0, time.UTC)
	tests := []struct {
		daysAhead int
		want      string
	}{
		{30, ""},
		{8, ""},
		{7, "expires in 7 days, on 10 Sep 2026"},
		{3, "expires in 3 days, on 06 Sep 2026"},
		{1, "expires tomorrow, 04 Sep 2026"},
		{0, "expires today, 03 Sep 2026"},
		{-1, "expired on 02 Sep 2026 (TOGGL_API_TOKEN_EXPIRES_AT)"},
	}
	for _, tt := range tests {
		tg := &Toggl{keyExpiresAt: today.AddDate(0, 0, tt.daysAhead)}
		s := tg.KeyStatus(today)
		if tt.want == "" && s.Warning != "" {
			t.Errorf("%d days ahead: warning = %q, want none", tt.daysAhead, s.Warning)
		}
		if tt.want != "" && !strings.Contains(s.Warning, tt.want) {
			t.Errorf("%d days ahead: warning = %q, want it to contain %q", tt.daysAhead, s.Warning, tt.want)
		}
		if s.Expired != (tt.daysAhead < 0) {
			t.Errorf("%d days ahead: Expired = %v", tt.daysAhead, s.Expired)
		}
	}
}

func TestKeyWarningIsSilentWithoutADateOrARejection(t *testing.T) {
	tg := &Toggl{}
	if s := tg.KeyStatus(time.Now()); s.Warning != "" || s.Expired || s.Rejected {
		t.Errorf("KeyStatus = %+v, want zero", s)
	}
	var none *Toggl
	if s := none.KeyStatus(time.Now()); s != (KeyStatus{}) {
		t.Errorf("nil client KeyStatus = %+v, want zero", s)
	}
}

func TestKeyWarningIsMeasuredInWholeDaysAcrossZones(t *testing.T) {
	vienna, _ := time.LoadLocation("Europe/Vienna")
	today := time.Date(2026, 9, 3, 23, 30, 0, 0, vienna)
	tg := &Toggl{keyExpiresAt: time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)}
	if got := tg.KeyStatus(today).Warning; !strings.Contains(got, "expires tomorrow") {
		t.Errorf("warning = %q, want tomorrow (a date, not 30 minutes)", got)
	}
}
