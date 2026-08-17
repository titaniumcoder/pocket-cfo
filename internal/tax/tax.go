package tax

import (
	"errors"
	"fmt"
	"strings"
)

type RegimeKey string

const (
	DomesticStandard       RegimeKey = "domestic_standard"
	EUB2BReverseCharge     RegimeKey = "eu_b2b_reverse_charge"
	OutsideEUPlaceOfSupply RegimeKey = "outside_eu_place_of_supply"
)

type Evidence string

const (
	EvidenceNone           Evidence = ""
	EvidenceVIES           Evidence = "vies"
	EvidenceManualDocument Evidence = "manual_document"
)

type Regime struct {
	Key      RegimeKey
	VATRate  int
	VIES     bool
	Evidence Evidence
}

type Issuer struct {
	CountryCode string
	VATID       string
}

type Recipient struct {
	CountryCode string
	VATID       string
	IsBusiness  bool
}

var (
	ErrConsumer = errors.New("tax: the recipient is not a business")

	ErrMissingVATID   = errors.New("tax: an EU business recipient has no VAT ID")
	ErrMalformedVATID = errors.New("tax: the VAT ID does not begin with the recipient's country code")

	ErrUnsupportedIssuer = errors.New("tax: the issuer is not established in the EU")

	ErrNoCountry = errors.New("tax: a party has no country code")
)

func Resolve(iss Issuer, rec Recipient) (Regime, error) {
	issCountry := strings.ToUpper(strings.TrimSpace(iss.CountryCode))
	recCountry := strings.ToUpper(strings.TrimSpace(rec.CountryCode))
	if issCountry == "" || recCountry == "" {
		return Regime{}, fmt.Errorf("%w: issuer %q, recipient %q", ErrNoCountry, iss.CountryCode, rec.CountryCode)
	}

	if !rec.IsBusiness {
		return Regime{}, fmt.Errorf("%w: recipient in %s is a private individual, and this company invoices businesses only",
			ErrConsumer, recCountry)
	}

	if recCountry == issCountry {
		return Regime{Key: DomesticStandard, VATRate: 20, VIES: false, Evidence: EvidenceNone}, nil
	}

	if !IsEU(issCountry) {
		return Regime{}, fmt.Errorf("%w: issuer is in %s, and the note catalog cites Bulgarian law throughout",
			ErrUnsupportedIssuer, issCountry)
	}

	if IsEU(recCountry) {
		vatID := strings.ToUpper(strings.ReplaceAll(rec.VATID, " ", ""))
		if vatID == "" {
			return Regime{}, fmt.Errorf("%w: %s business, so reverse charge cannot be established", ErrMissingVATID, recCountry)
		}
		if !VATIDLooksNational(recCountry, vatID) {
			return Regime{}, fmt.Errorf("%w: recipient is in %s but the VAT ID is %q", ErrMalformedVATID, recCountry, rec.VATID)
		}
		return Regime{Key: EUB2BReverseCharge, VATRate: 0, VIES: true, Evidence: EvidenceVIES}, nil
	}

	return Regime{Key: OutsideEUPlaceOfSupply, VATRate: 0, VIES: false, Evidence: EvidenceManualDocument}, nil
}

func VATIDLooksNational(countryCode, vatID string) bool {
	prefix := strings.ToUpper(countryCode)
	if prefix == "GR" {
		prefix = "EL"
	}
	return strings.HasPrefix(strings.ToUpper(vatID), prefix)
}
