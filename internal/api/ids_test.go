package api

import (
	"regexp"
	"testing"
)

var idShape = regexp.MustCompile(`^[0-9a-f]{8}(-[0-9]+)?$`)

func TestDerivedIDsAreShortStableAndInputSensitive(t *testing.T) {
	line := IDLine{Account: "Private Checking", Date: "2026-08-14", Amount: 12.5, Description: "PARKMART 0042"}
	first := DeriveTransactionIDs([]IDLine{line})
	again := DeriveTransactionIDs([]IDLine{line})
	if first[0] != again[0] || !idShape.MatchString(first[0]) || len(first[0]) != 8 {
		t.Fatalf("the same line must always get the same 8-hex id, got %q then %q", first[0], again[0])
	}
	for name, other := range map[string]IDLine{
		"account":     {Account: "Company Checking", Date: line.Date, Amount: line.Amount, Description: line.Description},
		"date":        {Account: line.Account, Date: "2026-08-15", Amount: line.Amount, Description: line.Description},
		"amount":      {Account: line.Account, Date: line.Date, Amount: 12.51, Description: line.Description},
		"description": {Account: line.Account, Date: line.Date, Amount: line.Amount, Description: "PARKMART 0043"},
	} {
		if DeriveTransactionIDs([]IDLine{other})[0] == first[0] {
			t.Errorf("changing the %s must change the id", name)
		}
	}
}

func TestIdenticalLinesInOneBatchAreSuffixedInOrder(t *testing.T) {
	line := IDLine{Account: "Private Checking", Date: "2026-08-14", Amount: 3, Description: "COFFEE"}
	ids := DeriveTransactionIDs([]IDLine{line, line, {Account: "Other", Date: "2026-08-14", Amount: 3, Description: "COFFEE"}, line})
	if ids[1] != ids[0]+"-2" || ids[3] != ids[0]+"-3" {
		t.Errorf("repeats must count up from -2 in request order, got %v", ids)
	}
	if ids[2] == ids[0] || len(ids[2]) != 8 {
		t.Errorf("a different line is not a repeat, got %v", ids)
	}
}

func TestDeriveIDsRefusesAnIncompleteLine(t *testing.T) {
	s := &Service{}
	for _, bad := range []IDLine{
		{Date: "2026-08-14", Amount: 1, Description: "x"},
		{Account: "a", Date: "14.08.2026", Amount: 1, Description: "x"},
		{Account: "a", Date: "2026-08-14", Amount: 1},
	} {
		_, err := s.DeriveIDs(DeriveIDsRequest{Lines: []IDLine{bad}})
		if e, ok := err.(*Error); !ok || e.Code != CodeInvalidRequest {
			t.Errorf("%+v: want invalid_request, got %v", bad, err)
		}
	}
	if _, err := s.DeriveIDs(DeriveIDsRequest{}); err == nil {
		t.Error("an empty request must be refused")
	}
}
