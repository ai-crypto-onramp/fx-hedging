package grpc

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTempFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

func TestBuildServerTLSConfigBadPath(t *testing.T) {
	_, err := buildServerTLSConfig(filepath.Join(t.TempDir(), "missing.pem"))
	if err == nil {
		t.Fatal("expected error for missing ca cert")
	}
}

func TestBuildServerTLSConfigBadPEM(t *testing.T) {
	p := writeTempFile(t, "bad.pem", []byte("not a cert"))
	if _, err := buildServerTLSConfig(p); err == nil {
		t.Fatal("expected error for unparseable ca cert")
	}
}

func TestBuildServerTLSConfigCAOnly(t *testing.T) {
	caPath := genCACertPath(t)
	cfg, err := buildServerTLSConfig(caPath)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if cfg.ClientAuth != 4 { // tls.RequireAndVerifyClientCert
		t.Fatalf("client auth = %v", cfg.ClientAuth)
	}
	if len(cfg.Certificates) != 0 {
		t.Fatalf("expected no server cert, got %d", len(cfg.Certificates))
	}
}

func TestBuildServerTLSConfigCertWithoutKey(t *testing.T) {
	caPath := genCACertPath(t)
	t.Setenv("SERVER_TLS_CERT", "/some/cert.pem")
	t.Setenv("SERVER_TLS_KEY", "")
	if _, err := buildServerTLSConfig(caPath); err == nil {
		t.Fatal("expected error for missing SERVER_TLS_KEY")
	}
}

func TestBuildServerTLSConfigCertLoadFails(t *testing.T) {
	caPath := genCACertPath(t)
	t.Setenv("SERVER_TLS_CERT", "/nonexistent/cert.pem")
	t.Setenv("SERVER_TLS_KEY", "/nonexistent/key.pem")
	if _, err := buildServerTLSConfig(caPath); err == nil {
		t.Fatal("expected error for missing cert/key files")
	}
}