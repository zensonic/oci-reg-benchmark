// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/zensonic/oci-reg-benchmark/internal/protocol"
)

// controlClient talks to the controller's agent API (see internal/protocol).
type controlClient struct {
	baseURL string
	runUID  string
	agent   int
	token   string
	http    *http.Client
}

func newControlClient(opts Options) *controlClient {
	return &controlClient{
		baseURL: strings.TrimSuffix(opts.ControllerURL, "/"),
		runUID:  opts.RunUID,
		agent:   opts.AgentIndex,
		token:   opts.Token,
		http:    opts.HTTPClient,
	}
}

// Plan fetches the run plan.
func (c *controlClient) Plan(ctx context.Context) (*protocol.Plan, error) {
	var plan protocol.Plan
	path := strings.ReplaceAll(protocol.PathPlan, "{uid}", c.runUID)
	if err := c.call(ctx, http.MethodGet, path, nil, &plan); err != nil {
		return nil, err
	}
	return &plan, nil
}

// Command asks what to do next.
func (c *controlClient) Command(ctx context.Context) (protocol.Command, error) {
	var cmd protocol.Command
	err := c.call(ctx, http.MethodGet, protocol.AgentPath(protocol.PathCommand, c.runUID, c.agent), nil, &cmd)
	return cmd, err
}

// State reports a finished step.
func (c *controlClient) State(ctx context.Context, update protocol.StateUpdate) error {
	return c.call(ctx, http.MethodPost, protocol.AgentPath(protocol.PathState, c.runUID, c.agent), update, nil)
}

// Report sends one interval report.
func (c *controlClient) Report(ctx context.Context, report protocol.IntervalReport) error {
	return c.call(ctx, http.MethodPost, protocol.AgentPath(protocol.PathReport, c.runUID, c.agent), report, nil)
}

func (c *controlClient) call(ctx context.Context, method, path string, body, into any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s %s: status %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	if into != nil {
		return json.NewDecoder(resp.Body).Decode(into)
	}
	return nil
}

// retry calls fn until it succeeds, the context ends, or the time limit
// passes. It is used for messages that must not be lost.
func retry(ctx context.Context, limit time.Duration, fn func() error) error {
	deadline := time.Now().Add(limit)
	wait := 200 * time.Millisecond
	for {
		err := fn()
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		wait = min(wait*2, 5*time.Second)
	}
}
