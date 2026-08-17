package api

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/actualsdata"
	"github.com/titaniumcoder/pocket-cfo/internal/finance/tracker"
)

func movp(m actualsdata.Movement) *actualsdata.Movement { return &m }

// markedAugust holds one line marked as money crossing to the owner, so a
// write touching any OTHER line has something to lose.
const markedAugust = `{
  "month": "2026-08",
  "coverage": [{ "account": "A", "from": "2026-08-01", "to": "2026-08-31", "imported_at": "2026-09-01" }],
  "transactions": [
    {"id":"p1","date":"2026-08-03","description":"PARKMART SOFIA","amount":12.5,"account":"A","category":"` + idRent + `"},
    {"id":"draw","date":"2026-08-06","description":"To Rico Metzger","amount":5000,"account":"Company Checking","ignored":"owner draw","movement":"owner_draw"},
    {"id":"arriving","date":"2026-08-06","description":"From TITANIUM CODER EOOD","amount":-5000,"account":"Private Checking","ignored":"the same transfer, seen from the other side"}
  ]
}`

// TestRewritingAMonthKeepsTheMovementMarkerOnEveryOtherLine is the
// highest-consequence bug this feature could have had. marshalMonth rebuilds
// the whole document field by field, so a field it does not know about is not
// preserved — it is silently dropped from every line in the month, through a
// write path otherwise proven not to lose anything.
func TestRewritingAMonthKeepsTheMovementMarkerOnEveryOtherLine(t *testing.T) {
	gh := newFakeGitHub(map[string]string{augustPath: markedAugust})
	s := writeService(t, gh)

	// An edit to an entirely different line.
	if _, err := edit(t, s, EditRequest{Edits: []Edit{
		{ID: "p1", Month: "2026-08", Category: strp(idGroceries)},
	}}); err != nil {
		t.Fatalf("EditTransactions: %v", err)
	}

	draw := lineIn(t, committed(t, gh, augustPath), "draw")
	if draw.Movement == nil {
		t.Fatal("editing another line stripped the marker from the draw — the loan would silently stop being settled by it")
	}
	if *draw.Movement != actualsdata.MovementOwnerDraw {
		t.Errorf("movement = %q, want it untouched", *draw.Movement)
	}
}

// TestReattributingAMarkedLineToACategoryClearsItsMarker: an edit replaces
// what a line carried, wholesale. A marker left behind would settle the
// director's loan against a line that is now groceries.
func TestReattributingAMarkedLineToACategoryClearsItsMarker(t *testing.T) {
	gh := newFakeGitHub(map[string]string{augustPath: markedAugust})
	s := writeService(t, gh)

	if _, err := edit(t, s, EditRequest{Edits: []Edit{
		{ID: "draw", Month: "2026-08", Category: strp(idGroceries)},
	}}); err != nil {
		t.Fatalf("EditTransactions: %v", err)
	}

	line := lineIn(t, committed(t, gh, augustPath), "draw")
	if line.Movement != nil {
		t.Errorf("the marker survived re-attribution: %q", *line.Movement)
	}
	if line.Category == nil || *line.Category != idGroceries {
		t.Errorf("the line was not re-attributed: %+v", line)
	}
}

// TestAMovementWithoutAnIgnoredReasonIsRefusedByTheWriteSurface: the same rule
// the file itself keeps, refused before anything is committed rather than
// after.
func TestAMovementWithoutAnIgnoredReasonIsRefusedByTheWriteSurface(t *testing.T) {
	gh := newFakeGitHub(map[string]string{augustPath: markedAugust})
	s := writeService(t, gh)

	_, err := edit(t, s, EditRequest{Edits: []Edit{
		{ID: "p1", Month: "2026-08", Movement: movp(actualsdata.MovementOwnerDraw)},
	}})
	if err == nil {
		t.Fatal("a marker with no ignored reason was accepted")
	}
	if !strings.Contains(err.Error(), "ignored reason") {
		t.Errorf("error = %q, want it to name what is missing", err)
	}
}

// TestTheMirrorLineIsRefusedByTheWriteSurfaceToo: recording it once is
// enforced wherever the file is written, not only where it is read.
func TestTheMirrorLineIsRefusedByTheWriteSurfaceToo(t *testing.T) {
	gh := newFakeGitHub(map[string]string{augustPath: markedAugust})
	s := writeService(t, gh)

	_, err := edit(t, s, EditRequest{Edits: []Edit{
		{ID: "arriving", Month: "2026-08", Ignored: strp("owner draw arriving"),
			Movement: movp(actualsdata.MovementOwnerDraw)},
	}})
	if err == nil {
		t.Fatal("a draw marked on money arriving was committed, so the transfer would count twice")
	}
	if !strings.Contains(err.Error(), "mark the company side instead") {
		t.Errorf("error = %q, want it to say where the marker belongs", err)
	}
}

// TestSearchReportsTheMovementSoHermesDoesNotMarkItTwice, and only_movements
// finds every crossing without reading each month's document.
func TestSearchReportsTheMovementSoHermesDoesNotMarkItTwice(t *testing.T) {
	s := writeService(t, newFakeGitHub(map[string]string{augustPath: markedAugust}))
	s.Actuals = &tracker.Actuals{FS: fstest.MapFS{
		"actuals/2026-08.json": &fstest.MapFile{Data: []byte(markedAugust)},
	}}

	all, err := s.Search(t.Context(), SearchQuery{IncludeIgnored: true, Years: []int{2026}})
	if err != nil {
		t.Fatal(err)
	}
	var seen string
	for _, tx := range all.Transactions {
		if tx.ID == "draw" {
			seen = tx.Movement
		}
	}
	if seen != string(actualsdata.MovementOwnerDraw) {
		t.Errorf("search reported movement %q for the draw, want it named so Hermes can see it is already marked", seen)
	}

	only, err := s.Search(t.Context(), SearchQuery{OnlyMovements: true, IncludeIgnored: true, Years: []int{2026}})
	if err != nil {
		t.Fatal(err)
	}
	if len(only.Transactions) != 1 || only.Transactions[0].ID != "draw" {
		t.Errorf("only_movements returned %d line(s), want just the marked one", len(only.Transactions))
	}
}
