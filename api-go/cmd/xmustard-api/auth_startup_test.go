package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xmustard/api-go/internal/workspaceops"
)

func writeSelfSignedCert(t *testing.T) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "xmustard-test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalECPrivateKey(key)
	dir := t.TempDir()
	certPath, keyPath = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	_ = os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	_ = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600)
	return certPath, keyPath
}

// externalIPv4 returns a non-loopback interface address, if the host has one, so an
// exposed listener is reached over a real non-loopback path.
func externalIPv4() string {
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLoopback() && ipn.IP.To4() != nil && !ipn.IP.IsLinkLocalUnicast() {
			return ipn.IP.String()
		}
	}
	return ""
}

// Audit Go #3: the complete startup matrix, against the real binary and a real
// listener. A non-loopback bind starts only with XMUSTARD_AUTH=required, configured
// credentials and TLS (or the explicit insecure-bind override); `off` never passes on
// a non-loopback bind; `required` without credentials never starts. Every server that
// does start must reject unauthenticated requests to protected routes whenever
// credentials exist or auth is required.
func TestStartupAuthMatrixAgainstRealListener(t *testing.T) {
	cert, key := writeSelfSignedCert(t)
	ext := externalIPv4()
	protected := []struct{ method, path string }{
		{"GET", "/api/workspaces"},
		{"GET", "/api/settings"},
		{"GET", "/api/workspaces/ws/context/active"},
		{"POST", "/api/workspaces/ws/context"},
		{"GET", "/api/workspaces/ws/search?q=x"},
	}
	for _, host := range []string{"127.0.0.1", "0.0.0.0"} {
		for _, mode := range []string{"auto", "required", "off"} {
			for _, creds := range []bool{false, true} {
				for _, transport := range []string{"plain", "insecure-override", "tls"} {
					name := fmt.Sprintf("%s/%s/creds=%v/%s", host, mode, creds, transport)
					t.Run(name, func(t *testing.T) {
						dir := t.TempDir()
						token := ""
						if creds {
							var err error
							if token, err = workspaceops.MintToken(dir, "operator", "admin"); err != nil {
								t.Fatal(err)
							}
						}
						env := map[string]string{"XMUSTARD_DATA_DIR": dir, "XMUSTARD_API_HOST": host, "XMUSTARD_AUTH": mode}
						switch transport {
						case "insecure-override":
							env["XMUSTARD_ALLOW_INSECURE_BIND"] = "1"
						case "tls":
							env["XMUSTARD_API_TLS_CERT"], env["XMUSTARD_API_TLS_KEY"] = cert, key
						}
						exposed := host != "127.0.0.1"
						wantStart := true
						refusal := ""
						switch {
						case mode == "required" && !creds:
							wantStart, refusal = false, "XMUSTARD_AUTH=required but no credentials are configured"
						case exposed && mode != "required":
							wantStart, refusal = false, "refusing non-loopback bind "+host+" with XMUSTARD_AUTH="+mode
						case exposed && transport == "plain":
							wantStart, refusal = false, "refusing non-loopback bind without TLS"
						}
						p := startAPIProc(t, env)
						started := false
						select {
						case <-p.exited:
						default:
							started = true
						}
						if started != wantStart {
							t.Fatalf("started=%v want %v; log:\n%s", started, wantStart, p.stderr.String())
						}
						if !started {
							// the exit must be the stated policy refusal, not an unrelated crash
							if !strings.Contains(p.stderr.String(), refusal) {
								t.Fatalf("refused without the expected policy message %q; log:\n%s", refusal, p.stderr.String())
							}
							return
						}
						bases := []string{p.base}
						if exposed && ext != "" {
							bases = append(bases, strings.Replace(p.base, "127.0.0.1", ext, 1))
						}
						enforced := mode == "required" || (mode == "auto" && creds)
						for _, base := range bases {
							for _, pr := range protected {
								req, _ := http.NewRequest(pr.method, base+pr.path, strings.NewReader(`{"content":"x"}`))
								resp, err := testClient.Do(req)
								if err != nil {
									t.Fatalf("%s %s: %v", pr.method, base+pr.path, err)
								}
								resp.Body.Close()
								if enforced && resp.StatusCode != http.StatusUnauthorized {
									t.Fatalf("unauthenticated %s %s reached a protected route: %d", pr.method, base+pr.path, resp.StatusCode)
								}
							}
							if enforced {
								req, _ := http.NewRequest("GET", base+"/api/workspaces", nil)
								req.Header.Set("Authorization", "Bearer "+token)
								resp, err := testClient.Do(req)
								if err != nil {
									t.Fatal(err)
								}
								resp.Body.Close()
								if resp.StatusCode != http.StatusOK {
									t.Fatalf("authenticated request rejected: %d", resp.StatusCode)
								}
							}
						}
					})
				}
			}
		}
	}
}
