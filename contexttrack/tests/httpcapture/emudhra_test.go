package httpcapture

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

// TestEMudhraDistrust checks the upstream verifier, without capture hooks,
// network access, or host roots. See testdata/emudhra/README.md for provenance.
func TestEMudhraDistrust(t *testing.T) {
	data, err := os.ReadFile("testdata/emudhra/dv-g3b-chain.pem")
	if err != nil {
		t.Fatal(err)
	}
	var certs []*x509.Certificate
	for len(bytes.TrimSpace(data)) > 0 {
		if !bytes.HasPrefix(bytes.TrimSpace(data), []byte("-----BEGIN CERTIFICATE-----")) {
			t.Fatal("fixture contains non-certificate data")
		}
		block, rest := pem.Decode(data)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			t.Fatal("fixture must contain only PEM certificates")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		certs = append(certs, cert)
		data = rest
	}
	if len(certs) != 3 {
		t.Fatalf("fixture has %d certificates, want 3 in leaf-first order", len(certs))
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(certs[1].RawSubjectPublicKeyInfo)); got != "ebed6475c2c2d80fcb199f3fe8dd6e6b3ce60b77eb15d4e42c3dec0c6f5b5105" {
		t.Fatalf("affected intermediate SPKI SHA-256 = %s", got)
	}

	// The final cross-signed certificate is intentionally a local trust anchor;
	// this does not test whether the host OS trusts its issuer.
	roots := x509.NewCertPool()
	roots.AddCert(certs[2])
	intermediates := x509.NewCertPool()
	intermediates.AddCert(certs[1])
	now := time.Date(2026, time.October, 21, 12, 0, 0, 0, time.UTC)

	t.Run("affected-chain", func(t *testing.T) {
		chains, err := certs[0].Verify(x509.VerifyOptions{
			Roots:         roots,
			Intermediates: intermediates,
			DNSName:       "dv-g3b-emudhra.we-have-a-problem.com",
			CurrentTime:   now,
			KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		})
		if len(chains) != 0 || err == nil {
			t.Fatalf("affected chain accepted: got %d chains, error %v", len(chains), err)
		}
		var invalid x509.CertificateInvalidError
		if !errors.As(err, &invalid) || invalid.Reason != x509.NoValidChains {
			t.Fatalf("expected CertificateInvalidError with NoValidChains, got %T: %v", err, err)
		}
	})

	t.Run("unaffected-anchor", func(t *testing.T) {
		chains, err := certs[2].Verify(x509.VerifyOptions{
			Roots:       roots,
			CurrentTime: now,
			KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
		})
		if err != nil || len(chains) == 0 {
			t.Fatalf("unaffected local anchor failed: got %d chains, error %v", len(chains), err)
		}
	})
}
