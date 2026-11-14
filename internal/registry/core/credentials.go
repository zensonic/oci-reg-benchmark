// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Credentials are the login details for one registry.
type Credentials struct {
	Username string
	Password string

	// IdentityToken is an OAuth2 refresh token, used by some registries
	// instead of a password.
	IdentityToken string

	// RegistryToken is a bearer token sent as-is.
	RegistryToken string
}

// IsEmpty reports whether no credentials are set (anonymous access).
func (c *Credentials) IsEmpty() bool {
	return c == nil || (c.Username == "" && c.Password == "" && c.IdentityToken == "" && c.RegistryToken == "")
}

// dockerConfig is the format of ~/.docker/config.json and of Kubernetes
// Secrets of type kubernetes.io/dockerconfigjson.
type dockerConfig struct {
	Auths map[string]dockerAuth `json:"auths"`
}

type dockerAuth struct {
	Username      string `json:"username"`
	Password      string `json:"password"`
	Auth          string `json:"auth"`
	IdentityToken string `json:"identitytoken"`
	RegistryToken string `json:"registrytoken"`
}

// LoadDockerConfig reads credentials for registryHost from a docker config
// file (AUTH-2). It returns nil credentials if the file has no entry for the
// host, which means anonymous access.
func LoadDockerConfig(path, registryHost string) (*Credentials, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading registry credentials: %w", err)
	}
	return ParseDockerConfig(data, registryHost)
}

// ParseDockerConfig parses docker config JSON and returns the credentials
// for registryHost, or nil if there are none.
func ParseDockerConfig(data []byte, registryHost string) (*Credentials, error) {
	var cfg dockerConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing docker config: %w", err)
	}

	want := normalizeHost(registryHost)
	for key, entry := range cfg.Auths {
		if normalizeHost(key) != want {
			continue
		}
		creds := &Credentials{
			Username:      entry.Username,
			Password:      entry.Password,
			IdentityToken: entry.IdentityToken,
			RegistryToken: entry.RegistryToken,
		}
		if entry.Auth != "" && creds.Username == "" {
			user, pass, err := decodeBasicAuth(entry.Auth)
			if err != nil {
				return nil, fmt.Errorf("docker config entry %q: %w", key, err)
			}
			creds.Username, creds.Password = user, pass
		}
		return creds, nil
	}
	return nil, nil
}

func decodeBasicAuth(encoded string) (user, pass string, err error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", "", errors.New("the auth field is not valid base64")
	}
	user, pass, found := strings.Cut(string(raw), ":")
	if !found {
		return "", "", errors.New("the auth field is not in user:password form")
	}
	return user, pass, nil
}

// normalizeHost turns "https://harbor.example.com/v1/" into "harbor.example.com".
func normalizeHost(s string) string {
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	host, _, _ := strings.Cut(s, "/")
	return strings.ToLower(host)
}

// TLSConfig builds the TLS settings for registry connections (AUTH-5).
// caFile may be empty. insecureSkipVerify turns off certificate checks.
func TLSConfig(caFile string, insecureSkipVerify bool) (*tls.Config, error) {
	cfg := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: insecureSkipVerify, //nolint:gosec // only when the user turns it on for test installations
	}
	if caFile == "" {
		return cfg, nil
	}

	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("reading CA certificates: %w", err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("no valid certificates found in %s", caFile)
	}
	cfg.RootCAs = pool
	return cfg, nil
}
