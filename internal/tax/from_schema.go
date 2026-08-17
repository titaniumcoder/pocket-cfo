package tax

import (
	"github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"
	"github.com/titaniumcoder/pocket-cfo/internal/schema/issuer"
	"github.com/titaniumcoder/pocket-cfo/internal/schema/recipient"
)

func IssuerOf(iss invoice.IssuerSnapshot) Issuer {
	return Issuer{CountryCode: iss.Address.CountryCode, VATID: iss.VatId}
}

func RecipientOf(rec invoice.RecipientSnapshot) Recipient {
	return Recipient{
		CountryCode: rec.Address.CountryCode,
		VATID:       deref(rec.VatId),
		IsBusiness:  rec.IsBusiness,
	}
}

func IssuerOfDocument(doc issuer.IssuerJson) Issuer {
	return Issuer{CountryCode: doc.Address.CountryCode, VATID: doc.VatId}
}

func RecipientOfDocument(doc recipient.RecipientJson) Recipient {
	return Recipient{
		CountryCode: doc.Address.CountryCode,
		VATID:       deref(doc.VatId),
		IsBusiness:  doc.IsBusiness,
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
