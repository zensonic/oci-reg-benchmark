// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// Authenticator adds credentials to registry requests (AUTH-1). Registry
// modules can supply their own; the default is the challenge-based
// authenticator below, which handles basic auth and the standard bearer token
// flow used by Harbor (AUTH-2).
type Authenticator interface {
	// Authorize adds credentials for the given scopes to the request. It may
	// fetch a token first; the time spent doing so is returned so it can be
	// recorded as the auth phase (AUTH-4, LAT-6).
	Authorize(ctx context.Context, req *http.Request, scopes []string) (time.Duration, error)

	// HandleChallenge is called when the registry answers 401. It learns the
	// auth scheme from the challenge, fetches a new token if needed, and
	// reports whether the request should be retried.
	HandleChallenge(ctx context.Context, resp *http.Response, scopes []string) (time.Duration, bool, error)
}

// Scope helpers for the bearer token flow.

// RepositoryScope returns a token scope for a repository, for example
// "repository:bench/app:pull,push".
func RepositoryScope(repository string, actions ...string) string {
	return fmt.Sprintf("repository:%s:%s", repository, strings.Join(actions, ","))
}

// CatalogScope is the token scope needed to list the catalog.
const CatalogScope = "registry:catalog:*"

// challengeAuthenticator implements basic auth and the bearer token flow.
// Each worker has its own instance, so tokens are cached per worker the way
// a real client caches them (AUTH-4).
type challengeAuthenticator struct {
	creds      *Credentials
	httpClient *http.Client
	userAgent  string

	mu      sync.Mutex
	scheme  string // "", "basic" or "bearer"
	realm   string
	service string
	tokens  map[string]cachedToken
}

type cachedToken struct {
	value   string
	expires time.Time
}

func newChallengeAuthenticator(creds *Credentials, httpClient *http.Client, userAgent string) *challengeAuthenticator {
	return &challengeAuthenticator{
		creds:      creds,
		httpClient: httpClient,
		userAgent:  userAgent,
		tokens:     map[string]cachedToken{},
	}
}

func (a *challengeAuthenticator) Authorize(ctx context.Context, req *http.Request, scopes []string) (time.Duration, error) {
	a.mu.Lock()
	scheme := a.scheme
	a.mu.Unlock()

	switch scheme {
	case "basic":
		if !a.creds.IsEmpty() {
			req.SetBasicAuth(a.creds.Username, a.creds.Password)
		}
		return 0, nil
	case "bearer":
		token, spent, err := a.token(ctx, scopes, false)
		if err != nil {
			return spent, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		return spent, nil
	default:
		// The scheme is not known yet. The first request goes out without
		// credentials and the registry's 401 challenge tells us the scheme.
		return 0, nil
	}
}

func (a *challengeAuthenticator) HandleChallenge(ctx context.Context, resp *http.Response, scopes []string) (time.Duration, bool, error) {
	scheme, params := parseChallenge(resp.Header.Get("WWW-Authenticate"))
	switch scheme {
	case "basic":
		a.mu.Lock()
		alreadyBasic := a.scheme == "basic"
		a.scheme = "basic"
		a.mu.Unlock()
		// Retry once with credentials. If we already sent them, they were rejected.
		return 0, !alreadyBasic && !a.creds.IsEmpty(), nil
	case "bearer":
		a.mu.Lock()
		a.scheme = "bearer"
		a.realm = params["realm"]
		a.service = params["service"]
		a.mu.Unlock()
		if params["scope"] != "" && !containsString(scopes, params["scope"]) {
			scopes = append(append([]string{}, scopes...), params["scope"])
		}
		_, spent, err := a.token(ctx, scopes, true)
		return spent, err == nil, err
	default:
		return 0, false, nil
	}
}

// token returns a cached token for the scopes, or fetches a new one.
func (a *challengeAuthenticator) token(ctx context.Context, scopes []string, forceRefresh bool) (string, time.Duration, error) {
	key := scopeKey(scopes)

	a.mu.Lock()
	cached, ok := a.tokens[key]
	realm, service := a.realm, a.service
	a.mu.Unlock()

	if ok && !forceRefresh && time.Now().Before(cached.expires) {
		return cached.value, 0, nil
	}
	if a.creds != nil && a.creds.RegistryToken != "" {
		return a.creds.RegistryToken, 0, nil
	}

	started := time.Now()
	value, lifetime, err := a.fetchToken(ctx, realm, service, scopes)
	spent := time.Since(started)
	if err != nil {
		return "", spent, err
	}

	a.mu.Lock()
	// Refresh a little before the registry's expiry time.
	a.tokens[key] = cachedToken{value: value, expires: started.Add(lifetime - 10*time.Second)}
	a.mu.Unlock()
	return value, spent, nil
}

// fetchToken asks the token service for a bearer token.
func (a *challengeAuthenticator) fetchToken(ctx context.Context, realm, service string, scopes []string) (string, time.Duration, error) {
	if realm == "" {
		return "", 0, errors.New("the registry asked for a bearer token but sent no realm")
	}
	tokenURL, err := url.Parse(realm)
	if err != nil {
		return "", 0, fmt.Errorf("invalid token realm %q: %w", realm, err)
	}
	query := tokenURL.Query()
	if service != "" {
		query.Set("service", service)
	}
	for _, scope := range scopes {
		query.Add("scope", scope)
	}
	tokenURL.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tokenURL.String(), nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("User-Agent", a.userAgent)
	if a.creds != nil && a.creds.Username != "" {
		req.SetBasicAuth(a.creds.Username, a.creds.Password)
	}

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("requesting token: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("requesting token: %w", newHTTPError(resp))
	}
	defer resp.Body.Close()

	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return "", 0, fmt.Errorf("decoding token response: %w", err)
	}
	token := body.Token
	if token == "" {
		token = body.AccessToken
	}
	if token == "" {
		return "", 0, errors.New("token response contained no token")
	}
	lifetime := time.Duration(body.ExpiresIn) * time.Second
	if lifetime < time.Minute {
		lifetime = time.Minute // the spec's default when expires_in is missing
	}
	return token, lifetime, nil
}

// parseChallenge parses a WWW-Authenticate header such as
// `Bearer realm="https://h/service/token",service="harbor-registry",scope="x"`.
func parseChallenge(header string) (scheme string, params map[string]string) {
	params = map[string]string{}
	scheme, rest, _ := strings.Cut(strings.TrimSpace(header), " ")
	scheme = strings.ToLower(scheme)

	for rest != "" {
		rest = strings.TrimLeft(rest, " ,")
		key, after, found := strings.Cut(rest, "=")
		if !found {
			break
		}
		key = strings.ToLower(strings.TrimSpace(key))

		var value string
		if strings.HasPrefix(after, `"`) {
			end := strings.Index(after[1:], `"`)
			if end < 0 {
				value, rest = after[1:], ""
			} else {
				value, rest = after[1:end+1], after[end+2:]
			}
		} else {
			value, rest, _ = strings.Cut(after, ",")
		}
		params[key] = value
	}
	return scheme, params
}

func scopeKey(scopes []string) string {
	sorted := append([]string{}, scopes...)
	sort.Strings(sorted)
	return strings.Join(sorted, " ")
}

func containsString(list []string, value string) bool {
	for _, v := range list {
		if v == value {
			return true
		}
	}
	return false
}
