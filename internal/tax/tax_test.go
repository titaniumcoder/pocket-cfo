package tax

import (
	"errors"
	"testing"

	"github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"
)

var bg = Issuer{CountryCode: "BG", VATID: "BG000000000"}

func business(country, vatID string) Recipient {
	return Recipient{CountryCode: country, VATID: vatID, IsBusiness: true}
}

func consumer(country string) Recipient {
	return Recipient{CountryCode: country, IsBusiness: false}
}

func TestResolve(t *testing.T) {
	tests := []struct {
		name    string
		iss     Issuer
		rec     Recipient
		want    Regime
		wantErr error
	}{
		{
			name: "CH business — outside the EU, place of supply follows the customer",
			iss:  bg, rec: business("CH", "CHE-000.000.000 MWST"),
			want: Regime{Key: OutsideEUPlaceOfSupply, VATRate: 0, VIES: false, Evidence: EvidenceManualDocument},
		},
		{
			name: "AT business — reverse charge, and VIES has to back it",
			iss:  bg, rec: business("AT", "ATU00000000"),
			want: Regime{Key: EUB2BReverseCharge, VATRate: 0, VIES: true, Evidence: EvidenceVIES},
		},
		{
			name: "DE business — reverse charge",
			iss:  bg, rec: business("DE", "DE123456789"),
			want: Regime{Key: EUB2BReverseCharge, VATRate: 0, VIES: true, Evidence: EvidenceVIES},
		},
		{
			name: "BG business — domestic, 20%",
			iss:  bg, rec: business("BG", "BG123456789"),
			want: Regime{Key: DomesticStandard, VATRate: 20, VIES: false, Evidence: EvidenceNone},
		},
		{
			name: "GB business — no longer in the EU, so no reverse charge",
			iss:  bg, rec: business("GB", "GB123456789"),
			want: Regime{Key: OutsideEUPlaceOfSupply, VATRate: 0, VIES: false, Evidence: EvidenceManualDocument},
		},
		{
			name: "US business — outside the EU",
			iss:  bg, rec: business("US", ""),
			want: Regime{Key: OutsideEUPlaceOfSupply, VATRate: 0, VIES: false, Evidence: EvidenceManualDocument},
		},

		{
			name: "DE consumer — blocked, never silently zero-rated",
			iss:  bg, rec: consumer("DE"), wantErr: ErrConsumer,
		},
		{
			name: "US consumer — blocked too; this company invoices businesses only",
			iss:  bg, rec: consumer("US"), wantErr: ErrConsumer,
		},
		{
			name: "BG consumer — blocked, even though 20% would have been right",
			iss:  bg, rec: consumer("BG"), wantErr: ErrConsumer,
		},
		{
			name: "DE business with no VAT ID — not a taxable person, so no reverse charge",
			iss:  bg, rec: business("DE", ""), wantErr: ErrMissingVATID,
		},
		{
			name: "DE business carrying an AT VAT ID — pasted onto the wrong recipient",
			iss:  bg, rec: business("DE", "ATU00000000"), wantErr: ErrMalformedVATID,
		},
		{
			name: "an issuer outside the EU — the catalog cites Bulgarian law throughout",
			iss:  Issuer{CountryCode: "US"}, rec: business("DE", "DE123456789"), wantErr: ErrUnsupportedIssuer,
		},
		{
			name: "no country at all",
			iss:  bg, rec: business("", "DE123456789"), wantErr: ErrNoCountry,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Resolve(tt.iss, tt.rec)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error = %v, want %v", err, tt.wantErr)
				}
				if got != (Regime{}) {
					t.Errorf("regime = %+v alongside an error, want the zero value", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("regime = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestVATIDLooksNational(t *testing.T) {
	tests := []struct {
		country, vatID string
		want           bool
	}{
		{"DE", "DE123456789", true},
		{"AT", "ATU00000000", true},
		{"at", "atu00000000", true},
		{"DE", "ATU00000000", false},
		{"GR", "EL123456789", true},
		{"GR", "GR123456789", false},
		{"DE", "", false},
	}
	for _, tt := range tests {
		if got := VATIDLooksNational(tt.country, tt.vatID); got != tt.want {
			t.Errorf("VATIDLooksNational(%q, %q) = %v, want %v", tt.country, tt.vatID, got, tt.want)
		}
	}
}

func TestRegimeKeysMatchTheInvoiceSchema(t *testing.T) {
	pairs := map[RegimeKey]invoice.TaxRegime{
		DomesticStandard:       invoice.TaxRegimeDomesticStandard,
		EUB2BReverseCharge:     invoice.TaxRegimeEuB2BReverseCharge,
		OutsideEUPlaceOfSupply: invoice.TaxRegimeOutsideEuPlaceOfSupply,
	}
	for ours, theirs := range pairs {
		if string(ours) != string(theirs) {
			t.Errorf("tax key %q does not match schema value %q", ours, theirs)
		}
	}
	if schema := invoice.TaxRegimes(); len(pairs) != len(schema) {
		t.Errorf("%d regimes here, %d in the schema (%v) — a regime was added on one side only", len(pairs), len(schema), schema)
	}
}
