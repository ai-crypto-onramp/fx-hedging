package grpc

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log"
	"net"
	"os"

	fxpb "github.com/ai-crypto-onramp/fx-hedging/proto/fx/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// NewServer constructs the gRPC server bound to grpcPort, registers the
// FX service, and returns (server, listener, error). When MTLS_CA_CERT is
// set the server requires and verifies client certificates (mTLS). When
// MTLS_CA_CERT is unset the server starts without TLS (local dev).
func NewServer(s *Services, grpcPort string) (*grpc.Server, net.Listener, error) {
	lis, err := net.Listen("tcp", ":"+grpcPort)
	if err != nil {
		return nil, nil, fmt.Errorf("grpc listen: %w", err)
	}
	var opts []grpc.ServerOption
	if caCertPath := os.Getenv("MTLS_CA_CERT"); caCertPath != "" {
		tlsCfg, err := buildServerTLSConfig(caCertPath)
		if err != nil {
			_ = lis.Close()
			return nil, nil, fmt.Errorf("grpc mtls: %w", err)
		}
		opts = append(opts, grpc.Creds(credentials.NewTLS(tlsCfg)))
		log.Printf("grpc: mTLS enabled (ca=%s)", caCertPath)
	}
	srv := grpc.NewServer(opts...)
	fxpb.RegisterFXServer(srv, NewAdapter(s))
	return srv, lis, nil
}

func buildServerTLSConfig(caCertPath string) (*tls.Config, error) {
	caPEM, err := os.ReadFile(caCertPath)
	if err != nil {
		return nil, fmt.Errorf("read ca cert: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("failed to parse ca cert")
	}
	cfg := &tls.Config{
		ClientCAs:  pool,
		ClientAuth: tls.RequireAndVerifyClientCert,
		MinVersion: tls.VersionTLS12,
	}
	if certPath := os.Getenv("SERVER_TLS_CERT"); certPath != "" {
		keyPath := os.Getenv("SERVER_TLS_KEY")
		if keyPath == "" {
			return nil, errors.New("SERVER_TLS_CERT set but SERVER_TLS_KEY missing")
		}
		cert, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return nil, fmt.Errorf("load server keypair: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	return cfg, nil
}
