package internal

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

func writePEM(t *testing.T, path, typ string, der []byte) {
	t.Helper()
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
}

// meshTLSFixture generates a test CA plus a server certificate (written to
// MUXCORE_TLS_* env) and returns a client tls.Config with its own certificate.
func meshTLSFixture(t *testing.T) *tls.Config {
	t.Helper()
	dir := t.TempDir()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTpl, caTpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, _ := x509.ParseCertificate(caDER)
	issue := func(name string, usage x509.ExtKeyUsage) tls.Certificate {
		key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		tpl := &x509.Certificate{
			SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name},
			NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
			DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
			KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage},
		}
		der, err := x509.CreateCertificate(rand.Reader, tpl, caCert, &key.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	}
	srv := issue("server", x509.ExtKeyUsageServerAuth)
	cli := issue("client", x509.ExtKeyUsageClientAuth)

	skey := srv.PrivateKey.(*ecdsa.PrivateKey)
	skd, _ := x509.MarshalECPrivateKey(skey)
	writePEM(t, filepath.Join(dir, "ca.pem"), "CERTIFICATE", caDER)
	writePEM(t, filepath.Join(dir, "srv.pem"), "CERTIFICATE", srv.Certificate[0])
	writePEM(t, filepath.Join(dir, "srv.key"), "EC PRIVATE KEY", skd)
	t.Setenv("MUXCORE_INSECURE_DISABLE_TLS", "")
	t.Setenv("MUXCORE_DEV_TLS_SKIP", "")
	t.Setenv("MUXCORE_GRPC_INSECURE", "")
	t.Setenv("MUXCORE_TLS_CA", filepath.Join(dir, "ca.pem"))
	t.Setenv("MUXCORE_TLS_CERT", filepath.Join(dir, "srv.pem"))
	t.Setenv("MUXCORE_TLS_KEY", filepath.Join(dir, "srv.key"))

	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	return &tls.Config{RootCAs: pool, ServerName: "localhost", Certificates: []tls.Certificate{cli}, MinVersion: tls.VersionTLS12}
}

// assertMeshTLS checks that addr accepts a TLS client holding a CA-signed
// certificate and rejects a plaintext client.
func assertMeshTLS(t *testing.T, addr string, clientCfg *tls.Config) {
	t.Helper()
	call := func(opt grpc.DialOption) error {
		conn, err := grpc.NewClient(addr, opt)
		if err != nil {
			return err
		}
		defer func() { _ = conn.Close() }()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return conn.Invoke(ctx, "/muxcore.test.Probe/Ping", &emptypb.Empty{}, &emptypb.Empty{})
	}
	// A completed TLS handshake reaches the server, which answers Unimplemented.
	if err := call(grpc.WithTransportCredentials(credentials.NewTLS(clientCfg))); status.Code(err) != codes.Unimplemented {
		t.Fatalf("TLS client: want Unimplemented (handshake ok), got %v", err)
	}
	if err := call(grpc.WithTransportCredentials(insecure.NewCredentials())); status.Code(err) == codes.Unimplemented || err == nil {
		t.Fatalf("plaintext client must be rejected, got %v", err)
	}
}
