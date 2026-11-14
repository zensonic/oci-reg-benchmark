// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ListResult is the outcome of a paginated listing.
type ListResult struct {
	// Entries is the number of tags or repositories returned in total.
	Entries int
	// Pages is the number of pages fetched (OP-11).
	Pages int
	// FirstByte is when the first byte of the first page arrived (LAT-4).
	FirstByte time.Time
	// Names are the returned names, if the caller asked to keep them.
	Names []string
}

// ListTags lists all tags of a repository, following pagination (OP-6).
func (c *Client) ListTags(ctx context.Context, timing *Timing, repository string, pageSize int, keepNames bool) (*ListResult, error) {
	return c.list(ctx, timing, repoPath(repository, "tags/list"), pageSize, keepNames,
		[]string{RepositoryScope(repository, "pull")}, "tags")
}

// Catalog lists all repositories in the registry, following pagination (OP-7).
func (c *Client) Catalog(ctx context.Context, timing *Timing, pageSize int, keepNames bool) (*ListResult, error) {
	return c.list(ctx, timing, "/v2/_catalog", pageSize, keepNames, []string{CatalogScope}, "repositories")
}

// list fetches every page of a listing. The registry links to the next page
// with a Link header.
func (c *Client) list(ctx context.Context, timing *Timing, path string, pageSize int, keepNames bool, scopes []string, field string) (*ListResult, error) {
	result := &ListResult{}
	next := c.base.ResolveReference(&url.URL{Path: path, RawQuery: url.Values{"n": {strconv.Itoa(pageSize)}}.Encode()})

	for next != nil {
		resp, err := c.do(ctx, timing, request{
			method: http.MethodGet,
			url:    next,
			scopes: scopes,
			expect: []int{http.StatusOK},
		})
		if err != nil {
			return nil, err
		}
		if result.Pages == 0 {
			result.FirstByte = resp.trace.FirstByte()
		}

		names, err := decodeNames(resp.Body, field)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		result.Pages++
		result.Entries += len(names)
		if keepNames {
			result.Names = append(result.Names, names...)
		}

		next = nextPage(next, resp.Header.Get("Link"))
	}
	return result, nil
}

func decodeNames(body io.Reader, field string) ([]string, error) {
	var page map[string]json.RawMessage
	if err := json.NewDecoder(io.LimitReader(body, 16<<20)).Decode(&page); err != nil {
		return nil, fmt.Errorf("decoding listing: %w", err)
	}
	var names []string
	if raw, ok := page[field]; ok && string(raw) != "null" {
		if err := json.Unmarshal(raw, &names); err != nil {
			return nil, fmt.Errorf("decoding listing: %w", err)
		}
	}
	return names, nil
}

// nextPage parses a Link header such as `</v2/_catalog?last=x&n=100>; rel="next"`.
func nextPage(current *url.URL, link string) *url.URL {
	for _, part := range strings.Split(link, ",") {
		target, params, found := strings.Cut(strings.TrimSpace(part), ";")
		if !found || !strings.Contains(params, `rel="next"`) {
			continue
		}
		target = strings.Trim(strings.TrimSpace(target), "<>")
		parsed, err := url.Parse(target)
		if err != nil {
			return nil
		}
		return current.ResolveReference(parsed)
	}
	return nil
}
