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

const defaultTSA = "https://freetsa.org/tsr"

type PDFSigner struct {
	Certificate *x509.Certificate
	Key         crypto.Signer
	TSAURL      string
}

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

func parsePrivateKey(pemBytes []byte, password string) (crypto.Signer, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found")
	}

	der := block.Bytes
	if password != "" {
		if x509.IsEncryptedPEMBlock(block) {
			var err error
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
