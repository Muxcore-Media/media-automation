package internal

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

const peerDiscoveryTimeout = 8 * time.Second

func meshInsecure() bool {
	if os.Getenv("MUXCORE_INSECURE_DISABLE_TLS") == "true" || os.Getenv("MUXCORE_GRPC_INSECURE") == "true" {
		return true
	}
	return false
}

func peerTransportCredentials() (credentials.TransportCredentials, error) {
	if meshInsecure() {
		return insecure.NewCredentials(), nil
	}
	certFile := os.Getenv("MUXCORE_TLS_CERT")
	keyFile := os.Getenv("MUXCORE_TLS_KEY")
	caFile := os.Getenv("MUXCORE_TLS_CA")
	if certFile == "" || keyFile == "" {
		return nil, fmt.Errorf("TLS required for peer dial — set MUXCORE_TLS_CERT/MUXCORE_TLS_KEY or MUXCORE_INSECURE_DISABLE_TLS=true")
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load peer TLS cert/key: %w", err)
	}
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}
	if caFile != "" {
		pemBytes, err := os.ReadFile(caFile) //nolint:gosec // path from operator config
		if err != nil {
			return nil, fmt.Errorf("read peer TLS CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pemBytes) {
			return nil, fmt.Errorf("parse peer TLS CA from %q", caFile)
		}
		tlsConfig.RootCAs = pool
	}
	return credentials.NewTLS(tlsConfig), nil
}

func dialPeer(addr string) (*grpc.ClientConn, error) {
	creds, err := peerTransportCredentials()
	if err != nil {
		return nil, err
	}
	return grpc.NewClient(addr, grpc.WithTransportCredentials(creds))
}

// withPeerTimeout applies a default deadline when the caller did not set one.
func withPeerTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, d)
}
