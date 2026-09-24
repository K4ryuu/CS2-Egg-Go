// SPDX-License-Identifier: GPL-3.0-or-later

package vpksync_test

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/vpksync"
)

const wingsYML = `debug: false
uuid: 1234
token_id: abc
token: "SECRETTOKEN"
api:
  host: 0.0.0.0
  port: 8080
  ssl:
    enabled: false
    cert: /etc/letsencrypt/live/node/fullchain.pem
  upload_limit: 100
system:
  root_directory: /var/lib/pterodactyl
remote: https://panel.example
`

func TestParseWings(t *testing.T) {
	w, err := vpksync.ParseWings(wingsYML)
	if err != nil {
		t.Fatal(err)
	}
	if w.Token != "SECRETTOKEN" || w.URL != "http://127.0.0.1:8080" || w.CertPath != "/etc/letsencrypt/live/node/fullchain.pem" {
		t.Fatalf("%+v", w)
	}
	w, _ = vpksync.ParseWings("token: t\napi:\n  host: node.example\n  port: 8443\n  ssl:\n    enabled: true\n")
	if w.URL != "https://node.example:8443" {
		t.Fatalf("ssl url: %s", w.URL)
	}
	if _, err := vpksync.ParseWings("api:\n  host: x\n"); err == nil {
		t.Fatal("missing token must fail")
	}
}

func TestRestartPinsTheWingsCertificate(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer srv.Close()
	certFile := filepath.Join(t.TempDir(), "cert.pem")
	os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600)
	w := vpksync.Wings{Token: "tok", URL: srv.URL, CertPath: certFile}
	if err := w.Restart(context.Background(), "u"); err != nil {
		t.Fatalf("pinned cert must be accepted: %v", err)
	}
	other := filepath.Join(t.TempDir(), "other.pem")
	os.WriteFile(other, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte{1, 2, 3}}), 0o600)
	w.CertPath = other
	if err := w.Restart(context.Background(), "u"); err == nil {
		t.Fatal("a different certificate must be refused")
	}
	w.CertPath = "/nonexistent.pem"
	if err := w.Restart(context.Background(), "u"); err == nil {
		t.Fatal("no cert file = no call")
	}
}

func TestRestartCallsThePowerEndpoint(t *testing.T) {
	var got *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		w.WriteHeader(204)
	}))
	defer srv.Close()
	w := vpksync.Wings{Token: "tok", URL: srv.URL}
	if err := w.Restart(context.Background(), "uuid-1"); err != nil {
		t.Fatal(err)
	}
	if got.URL.Path != "/api/servers/uuid-1/power" || got.Header.Get("Authorization") != "Bearer tok" || got.Method != "POST" {
		t.Fatalf("request: %s %s %v", got.Method, got.URL.Path, got.Header)
	}
}
