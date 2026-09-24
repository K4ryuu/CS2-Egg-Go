// SPDX-License-Identifier: GPL-3.0-or-later

package vpksync

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

// WingsConfigPaths are tried in order when no path is configured. Pelican
// ships the same schema under its own directory.
var WingsConfigPaths = []string{"/etc/pterodactyl/config.yml", "/etc/pelican/config.yml"}

// Wings is what the restart call needs.
type Wings struct {
	Token    string
	URL      string // scheme://host:port
	CertPath string // api.ssl.cert, pinned for the TLS check when set
}

// FindWingsConfig returns the first readable config path.
func FindWingsConfig(override string) (string, error) {
	paths := WingsConfigPaths
	if override != "" {
		paths = []string{override}
	}
	for _, p := range paths {
		if f, err := os.Open(p); err == nil {
			f.Close()
			return p, nil
		}
	}
	return "", fmt.Errorf("wings config not found (looked for %s); cs2node must run as root, config.yml is root-only", strings.Join(paths, ", "))
}

var (
	tokenLine = regexp.MustCompile(`(?m)^\s*token:\s*"?([^"\s]+)"?`)
	apiBlock  = regexp.MustCompile(`(?ms)^api:\n(.*?)(?:^[a-z]|\z)`)
	hostLine  = regexp.MustCompile(`(?m)^\s+host:\s*"?([^"\s]+)"?`)
	portLine  = regexp.MustCompile(`(?m)^\s+port:\s*"?([0-9]+)"?`)
	sslBlock  = regexp.MustCompile(`(?ms)^\s+ssl:\n(.*?)(?:^\s{0,2}[a-z]|\z)`)
	enabled   = regexp.MustCompile(`(?m)^\s+enabled:\s*"?(true|false)"?`)
	certLine  = regexp.MustCompile(`(?m)^\s+cert:\s*"?([^"\s]+)"?`)
)

// ParseWings extracts the token and API URL from a Wings config.yml the
// way the bash daemon did: the first token line, then host/port/ssl.enabled
// inside the api block. 0.0.0.0 becomes 127.0.0.1.
func ParseWings(yml string) (Wings, error) {
	m := tokenLine.FindStringSubmatch(yml)
	if m == nil {
		return Wings{}, errors.New("no api token in wings config")
	}
	w := Wings{Token: m[1]}
	host, port, ssl := "0.0.0.0", "8080", true
	if b := apiBlock.FindStringSubmatch(yml); b != nil {
		if h := hostLine.FindStringSubmatch(b[1]); h != nil {
			host = h[1]
		}
		if p := portLine.FindStringSubmatch(b[1]); p != nil {
			port = p[1]
		}
		if s := sslBlock.FindStringSubmatch(b[1]); s != nil {
			if e := enabled.FindStringSubmatch(s[1]); e != nil {
				ssl = e[1] == "true"
			}
			if c := certLine.FindStringSubmatch(s[1]); c != nil {
				w.CertPath = c[1]
			}
		}
	}
	if host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	scheme := "https"
	if !ssl {
		scheme = "http"
	}
	w.URL = scheme + "://" + host + ":" + port
	return w, nil
}

// LoadWings reads and parses the config.
func LoadWings(override string) (Wings, error) {
	p, err := FindWingsConfig(override)
	if err != nil {
		return Wings{}, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return Wings{}, err
	}
	return ParseWings(string(data))
}

// tlsConfig pins Wings' own certificate: the call goes to 127.0.0.1 while
// the cert names the node's FQDN, so hostname verification cannot apply.
// The presented leaf must equal the certificate file Wings is configured
// with; without a readable cert file the call is refused.
func (w Wings) tlsConfig() (*tls.Config, error) {
	pemData, err := os.ReadFile(w.CertPath)
	if err != nil {
		return nil, fmt.Errorf("wings ssl cert not readable (%s): %w", w.CertPath, err)
	}
	block, _ := pem.Decode(pemData)
	if block == nil {
		return nil, errors.New("wings ssl cert is not PEM")
	}
	pinned := block.Bytes
	return &tls.Config{
		InsecureSkipVerify: true, // replaced by the pin below
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 || !bytes.Equal(raw[0], pinned) {
				return errors.New("wings presented a certificate other than its configured one")
			}
			return nil
		},
	}, nil
}

// Restart asks Wings to restart the server (container name == uuid).
func (w Wings) Restart(ctx context.Context, uuid string) error {
	transport := &http.Transport{}
	if strings.HasPrefix(w.URL, "https://") {
		cfg, err := w.tlsConfig()
		if err != nil {
			return err
		}
		transport.TLSClientConfig = cfg
	}
	client := &http.Client{Timeout: 30 * time.Second, Transport: transport}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL+"/api/servers/"+uuid+"/power", bytes.NewBufferString(`{"action":"restart"}`))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+w.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case 200, 202, 204:
		return nil
	}
	return fmt.Errorf("wings answered HTTP %d", resp.StatusCode)
}
