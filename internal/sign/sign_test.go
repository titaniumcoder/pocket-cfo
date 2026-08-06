package sign

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/digitorus/pdfsign/verify"
)

// selfSignedCert generates a throwaway EC self-signed certificate and key,
// PEM-encoded, for testing only — never a real (B-Trust or otherwise)
// signing certificate.
func selfSignedCert(t *testing.T) (certPEM, keyPEM []byte, key *ecdsa.PrivateKey) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "invoicer test signer"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection},
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})

	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

	return certPEM, keyPEM, key
}

// repoRoot chdirs the test into the repository root, matching the
// convention in internal/render's tests. The fixture PDF path itself
// respects BUILD_DIR (see below) for the split layout where a companion
// repo runs this test against its own real data via env var.
func repoRoot(t *testing.T) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	if err := os.Chdir(filepath.Join("..", "..")); err != nil {
		t.Fatalf("chdir repo root: %v", err)
	}
}

func TestPDFSigner_Sign_CertifiesPDF(t *testing.T) {
	repoRoot(t)

	certPEM, keyPEM, _ := selfSignedCert(t)
	cert, err := parseCertificate(certPEM)
	if err != nil {
		t.Fatalf("parse test certificate: %v", err)
	}
	key, err := parsePrivateKey(keyPEM, "")
	if err != nil {
		t.Fatalf("parse test key: %v", err)
	}

	buildDir := os.Getenv("BUILD_DIR")
	if buildDir == "" {
		buildDir = "build"
	}
	in, err := os.ReadFile(filepath.Join(buildDir, "INV-0000000001.pdf"))
	if err != nil {
		t.Fatalf("read fixture pdf: %v", err)
	}

	signer := &PDFSigner{Certificate: cert, Key: key} // no TSAURL: unit test must not depend on freetsa.org
	out, err := signer.Sign(context.Background(), in)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	if bytes.Equal(in, out) {
		t.Fatal("signed output is byte-identical to the input")
	}
	if !bytes.HasPrefix(out, []byte("%PDF-")) {
		t.Fatal("signed output does not look like a PDF")
	}
	if !bytes.Contains(out, []byte("DocMDP")) {
		t.Error("signed output has no /DocMDP entry — certification signature did not apply")
	}

	resp, err := verify.Verify(bytes.NewReader(out), int64(len(out)))
	if err != nil {
		t.Fatalf("verify signed pdf: %v", err)
	}
	if len(resp.Signers) != 1 {
		t.Fatalf("got %d signers, want 1", len(resp.Signers))
	}
	if !resp.Signers[0].ValidSignature {
		t.Error("signature reported as not cryptographically valid")
	}
}

func TestNewFromEnv_NotConfigured(t *testing.T) {
	t.Setenv("SIGN_CERT_B64", "")
	t.Setenv("SIGN_KEY_B64", "")

	signer, ok, err := NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}
	if ok {
		t.Fatal("ok = true with no SIGN_CERT_B64 set")
	}
	if signer != nil {
		t.Fatal("signer != nil with no SIGN_CERT_B64 set")
	}
}

func TestNewFromEnv_MissingKey(t *testing.T) {
	certPEM, _, _ := selfSignedCert(t)
	t.Setenv("SIGN_CERT_B64", base64.StdEncoding.EncodeToString(certPEM))
	t.Setenv("SIGN_KEY_B64", "")

	_, ok, err := NewFromEnv()
	if err == nil {
		t.Fatal("expected an error when SIGN_KEY_B64 is missing")
	}
	if ok {
		t.Fatal("ok = true despite missing SIGN_KEY_B64")
	}
}

func TestNewFromEnv_Configured(t *testing.T) {
	certPEM, keyPEM, _ := selfSignedCert(t)
	t.Setenv("SIGN_CERT_B64", base64.StdEncoding.EncodeToString(certPEM))
	t.Setenv("SIGN_KEY_B64", base64.StdEncoding.EncodeToString(keyPEM))
	t.Setenv("SIGN_KEY_PASS", "")

	signer, ok, err := NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}
	if !ok {
		t.Fatal("ok = false with both secrets set")
	}
	if signer.Certificate == nil || signer.Key == nil {
		t.Fatal("signer missing certificate or key")
	}
	if signer.TSAURL != defaultTSA {
		t.Errorf("TSAURL = %q, want %q", signer.TSAURL, defaultTSA)
	}
}

func TestParsePrivateKey_EncryptedRSA(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	der := x509.MarshalPKCS1PrivateKey(rsaKey)

	//nolint:staticcheck // exercising the legacy encrypted-PEM path parsePrivateKey supports
	block, err := x509.EncryptPEMBlock(rand.Reader, "RSA PRIVATE KEY", der, []byte("s3cret"), x509.PEMCipherAES256)
	if err != nil {
		t.Fatalf("encrypt pem block: %v", err)
	}
	encPEM := pem.EncodeToMemory(block)

	if _, err := parsePrivateKey(encPEM, "wrong-password"); err == nil {
		t.Fatal("expected an error with the wrong password")
	}
	key, err := parsePrivateKey(encPEM, "s3cret")
	if err != nil {
		t.Fatalf("parsePrivateKey with correct password: %v", err)
	}
	if key == nil {
		t.Fatal("parsePrivateKey returned a nil key")
	}
}
