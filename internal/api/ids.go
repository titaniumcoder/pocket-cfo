package api

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
)

type IDLine struct {
	Account     string  `json:"account"`
	Date        string  `json:"date"`
	Amount      float64 `json:"amount"`
	Description string  `json:"description"`
}

type DeriveIDsRequest struct {
	Lines []IDLine `json:"lines"`
}

type DeriveIDsResult struct {
	IDs []string `json:"ids"`
}

const idHexLength = 8

func (s *Service) DeriveIDs(req DeriveIDsRequest) (*DeriveIDsResult, error) {
	if len(req.Lines) == 0 {
		return nil, errorf(CodeInvalidRequest, "send at least one line")
	}
	for i, l := range req.Lines {
		if l.Account == "" || l.Date == "" || l.Description == "" {
			return nil, errorf(CodeInvalidRequest, "line %d needs an account, a date and a description", i)
		}
		if err := checkDay(l.Date); err != nil {
			return nil, errorf(CodeInvalidRequest, "line %d: %v", i, err)
		}
	}
	return &DeriveIDsResult{IDs: DeriveTransactionIDs(req.Lines)}, nil
}

func DeriveTransactionIDs(lines []IDLine) []string {
	taken := map[string]int{}
	ids := make([]string, 0, len(lines))
	for _, l := range lines {
		base := lineHash(l)
		taken[base]++
		ids = append(ids, suffixed(base, taken[base]))
	}
	return ids
}

func lineHash(l IDLine) string {
	key := strings.Join([]string{l.Account, l.Date, strconv.FormatFloat(l.Amount, 'f', 2, 64), l.Description}, "\n")
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:idHexLength]
}

func suffixed(base string, occurrence int) string {
	if occurrence == 1 {
		return base
	}
	return base + "-" + strconv.Itoa(occurrence)
}
