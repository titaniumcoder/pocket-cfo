// Package sign certifies a rendered invoice PDF: a DocMDP certification
// signature that disallows any further change, so the PDF itself carries
// tamper evidence rather than a hash field. See ARCHITECTURE.md §6.
//
// Status: implemented and tested, but inert until SIGN_CERT_B64/SIGN_KEY_B64
// are set — see NewFromEnv. No production certificate exists yet; the plan
// is a B-Trust (Bulgarian QTSP) qualified certificate once a client actually
// requires a signed document.
package sign

import (
	"bytes"
	"context"
	"crypto"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"os"
	"time"

	"github.com/digitorus/pdf"
	pdfsign "github.com/digitorus/pdfsign/sign"
)

// defaultTSA is a free, public RFC3161 timestamp authority. Timestamping
// keeps the signature verifiable after the signing certificate's own
// validity period ends. See ARCHITECTURE.md §6.
const defaultTSA = "https://freetsa.org/tsr"

// PDFSigner certifies a PDF with a CertificationSignature and
// DoNotAllowAnyChangesPerms: any edit made after signing invalidates the
// signature in every conforming viewer.
type PDFSigner struct {
	Certificate *x509.Certificate
	Key         crypto.Signer
	TSAURL      string
}

// NewFromEnv builds a PDFSigner from SIGN_CERT_B64 and SIGN_KEY_B64
// (base64-encoded PEM) and an optional SIGN_KEY_PASS if the key PEM is
// password-encrypted. ok is false, with a nil error, when SIGN_CERT_B64 is
// unset — the caller then treats signing as a no-op, matching the
// API2PDF_KEY convention in cmd/pocket-cfo-ctl/render.go.
func NewFromEnv() (signer *PDFSigner, ok bool, err error) {
	certB64 := os.Getenv("SIGN_CERT_B64")
	if certB64 == "" {
		return nil, false, nil
	}
	keyB64 := os.Getenv("SIGN_KEY_B64")
	if keyB64 == "" {
		return nil, false, fmt.Errorf("sign: SIGN_CERT_B64 is set but SIGN_KEY_B64 is not")
	}

	certPEM, err := base64.StdEncoding.DecodeString(certB64)
	if err != nil {
		return nil, false, fmt.Errorf("sign: decode SIGN_CERT_B64: %w", err)
	}
	keyPEM, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil {
		return nil, false, fmt.Errorf("sign: decode SIGN_KEY_B64: %w", err)
	}

	cert, err := parseCertificate(certPEM)
	if err != nil {
		return nil, false, fmt.Errorf("sign: parse certificate: %w", err)
	}
	key, err := parsePrivateKey(keyPEM, os.Getenv("SIGN_KEY_PASS"))
	if err != nil {
		return nil, false, fmt.Errorf("sign: parse private key: %w", err)
	}

	return &PDFSigner{Certificate: cert, Key: key, TSAURL: defaultTSA}, true, nil
}

// Sign certifies pdfBytes in memory and returns the signed document. Key
// material never touches disk. ctx is accepted for symmetry with
// render.Renderer; digitorus/pdfsign has no native cancellation, so
// cancellation only takes effect before signing starts.
func (s *PDFSigner) Sign(ctx context.Context, pdfBytes []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	in := bytes.NewReader(pdfBytes)
	rdr, err := pdf.NewReader(in, int64(len(pdfBytes)))
	if err != nil {
		return nil, fmt.Errorf("sign: open pdf: %w", err)
	}

	var out bytes.Buffer
	err = pdfsign.Sign(in, &out, rdr, int64(len(pdfBytes)), pdfsign.SignData{
		Signature: pdfsign.SignDataSignature{
			CertType:   pdfsign.CertificationSignature,
			DocMDPPerm: pdfsign.DoNotAllowAnyChangesPerms,
			Info: pdfsign.SignDataSignatureInfo{
				Name:   s.Certificate.Subject.CommonName,
				Reason: "Invoice integrity",
				Date:   time.Now(),
			},
		},
		Signer:          s.Key,
		DigestAlgorithm: crypto.SHA256,
		Certificate:     s.Certificate,
		TSA:             pdfsign.TSA{URL: s.TSAURL},
	})
	if err != nil {
		return nil, fmt.Errorf("sign: %w", err)
	}
	return out.Bytes(), nil
}

func parseCertificate(pemBytes []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found")
	}
	return x509.ParseCertificate(block.Bytes)
}

// parsePrivateKey accepts PKCS8 (RSA or EC), PKCS1 (RSA) and SEC1 (EC) PEM
// keys, optionally encrypted with password per RFC 1423 — the format
// openssl produces for an encrypted key file. B-Trust's actual delivery
// format is unknown until a certificate is in hand; this may need to grow
// PKCS12 support then.
func parsePrivateKey(pemBytes []byte, password string) (crypto.Signer, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found")
	}

	der := block.Bytes
	if password != "" {
		//nolint:staticcheck // RFC 1423 PEM encryption is what openssl still emits for -passout; acceptable here since the ciphertext is base64'd into a secrets store, not transmitted.
		if x509.IsEncryptedPEMBlock(block) {
			var err error
			//nolint:staticcheck
			der, err = x509.DecryptPEMBlock(block, []byte(password))
			if err != nil {
				return nil, fmt.Errorf("decrypt private key: %w", err)
			}
		}
	}

	if key, err := x509.ParsePKCS8PrivateKey(der); err == nil {
		signer, ok := key.(crypto.Signer)
		if !ok {
			return nil, fmt.Errorf("PKCS8 key does not implement crypto.Signer")
		}
		return signer, nil
	}
	if key, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return key, nil
	}
	if key, err := x509.ParseECPrivateKey(der); err == nil {
		return key, nil
	}
	return nil, fmt.Errorf("unrecognized private key format (tried PKCS8, PKCS1, EC SEC1)")
}
