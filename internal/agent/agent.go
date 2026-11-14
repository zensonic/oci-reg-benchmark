// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

// Package agent runs one ORB agent: it fetches the run plan from the
// controller, prepares the test data, and then follows the controller's
// commands to seed, run each scenario's workers, and clean up. See the run
// lifecycle and agent API sections of docs/architecture.md.
package agent

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	orbv1 "github.com/zensonic/oci-reg-benchmark/api/v1alpha1"
	"github.com/zensonic/oci-reg-benchmark/internal/imageset"
	"github.com/zensonic/oci-reg-benchmark/internal/protocol"
	"github.com/zensonic/oci-reg-benchmark/internal/registry/core"
	"github.com/zensonic/oci-reg-benchmark/internal/registry/module"
	"github.com/zensonic/oci-reg-benchmark/internal/registry/modules"
	"github.com/zensonic/oci-reg-benchmark/internal/scenario"
	"github.com/zensonic/oci-reg-benchmark/internal/scenario/dedup"
	"github.com/zensonic/oci-reg-benchmark/internal/scenario/load"
)

// Run runs an agent configured from the environment (see OptionsFromEnv).
func Run(ctx context.Context) error {
	opts, err := OptionsFromEnv()
	if err != nil {
		return err
	}
	return RunWithOptions(ctx, opts)
}

// RunWithOptions runs an agent until the controller tells it to exit, the
// run is aborted, or a fatal error happens. A fatal error is reported to the
// controller before it is returned.
func RunWithOptions(ctx context.Context, opts Options) error {
	opts = opts.withDefaults()
	a := &agent{opts: opts, control: newControlClient(opts), logf: opts.Logf}

	err := retry(ctx, 2*time.Minute, func() error {
		plan, err := a.control.Plan(ctx)
		a.plan = plan
		return err
	})
	if err != nil {
		return fmt.Errorf("fetching the run plan: %w", err)
	}
	a.logf("agent %d of %d for run %s", opts.AgentIndex, a.plan.Agents, a.plan.RunName)

	if err := a.setup(ctx); err != nil {
		return a.fail(ctx, err)
	}
	if err := a.reportReady(ctx); err != nil {
		return err
	}
	return a.followCommands(ctx)
}

// agent is the state of one running agent.
type agent struct {
	opts    Options
	control *controlClient
	logf    func(string, ...any)

	plan      *protocol.Plan
	env       *scenario.Env
	scenarios []scenario.Scenario
}

// isLeader reports whether this is agent 0, which runs checks and cleanup.
func (a *agent) isLeader() bool { return a.opts.AgentIndex == 0 }

// setup builds the registry clients, downloads the static image set and
// creates the scenarios (the Preparing phase).
func (a *agent) setup(ctx context.Context) error {
	spec := &a.plan.Spec
	spec.Default()
	if err := spec.Validate(); err != nil {
		return fmt.Errorf("invalid benchmark: %w", err)
	}

	mod, err := modules.ByName(spec.Registry.Module)
	if err != nil {
		return err
	}
	registryURL, err := url.Parse(spec.Registry.URL)
	if err != nil {
		return fmt.Errorf("invalid registry URL: %w", err)
	}

	caFile := ""
	if fileExists(a.opts.CAFile) {
		caFile = a.opts.CAFile
	}
	tlsConfig, err := core.TLSConfig(caFile, spec.Registry.InsecureSkipVerify)
	if err != nil {
		return err
	}

	var creds *core.Credentials
	if fileExists(a.opts.RegistryCredentialsFile) {
		creds, err = core.LoadDockerConfig(a.opts.RegistryCredentialsFile, registryURL.Host)
		if err != nil {
			return err
		}
	}

	a.env = &scenario.Env{
		Plan:        a.plan,
		Agent:       a.opts.AgentIndex,
		Agents:      max(a.plan.Agents, 1),
		Module:      mod,
		Credentials: creds,
		Random:      imageset.NewRandom(spec.ImageSet.Random, spec.Seed),
		Layout:      scenario.Layout{Root: a.plan.RepositoryRoot, RunName: a.plan.RunName},
		NewClient: func() (*core.Client, error) {
			return core.New(core.Config{
				BaseURL:       spec.Registry.URL,
				Credentials:   creds,
				TLS:           tlsConfig.Clone(),
				Authenticator: mod.Authenticator(),
			})
		},
	}

	if spec.ImageSet.Type == orbv1.ImageSetStatic && !a.plan.CheckOnly {
		a.logf("downloading the static image set")
		dir := filepath.Join(a.opts.ScratchDir, "static")
		static, err := imageset.DownloadStatic(ctx, spec.ImageSet.Static, dir, a.sourceClientFor(registryURL.Host, creds, caFile))
		if err != nil {
			return err
		}
		a.env.Static = static
	}

	for _, s := range spec.Scenarios {
		sc, err := newScenario(a.env, s)
		if err != nil {
			return fmt.Errorf("scenario %q: %w", s.Name, err)
		}
		a.scenarios = append(a.scenarios, sc)
	}
	return nil
}

// newScenario creates a scenario of the spec's type. Adding a scenario type
// means adding its package and one case here (TYPE-4).
func newScenario(env *scenario.Env, spec orbv1.ScenarioSpec) (scenario.Scenario, error) {
	switch spec.Type {
	case orbv1.ScenarioLoad:
		return load.New(env, spec)
	case orbv1.ScenarioDeduplication:
		return dedup.New(env, spec)
	default:
		return nil, fmt.Errorf("unknown scenario type %q", spec.Type)
	}
}

// sourceClientFor returns a function that creates clients for the registries
// the static image set is downloaded from (IMG-7).
func (a *agent) sourceClientFor(targetHost string, targetCreds *core.Credentials, caFile string) imageset.ClientForHost {
	return func(host string) (*core.Client, error) {
		var creds *core.Credentials
		if fileExists(a.opts.SourceCredentialsFile) {
			for _, alias := range hostAliases(host) {
				found, err := core.LoadDockerConfig(a.opts.SourceCredentialsFile, alias)
				if err != nil {
					return nil, err
				}
				if found != nil {
					creds = found
					break
				}
			}
		}
		if creds == nil && host == targetHost {
			creds = targetCreds
		}
		tlsConfig, err := core.TLSConfig(caFile, false)
		if err != nil {
			return nil, err
		}
		return core.New(core.Config{BaseURL: "https://" + host, Credentials: creds, TLS: tlsConfig})
	}
}

// hostAliases returns the names a docker config may use for a host. Docker
// Hub credentials are usually stored under index.docker.io.
func hostAliases(host string) []string {
	if host == "registry-1.docker.io" {
		return []string{host, "index.docker.io", "docker.io"}
	}
	return []string{host}
}

// reportReady tells the controller that preparing is done. Agent 0 first
// runs the registry checks (REG-7); if one fails, the agent reports failure.
func (a *agent) reportReady(ctx context.Context) error {
	update := protocol.StateUpdate{State: protocol.StateReady}
	if a.isLeader() {
		checks, version, passed := a.runChecks(ctx)
		update.Checks, update.RegistryVersion = checks, version
		if a.env.Static != nil {
			update.ResolvedImages = a.env.Static.Resolved()
		}
		if !passed {
			update.State = protocol.StateFailed
			update.Message = "registry checks failed: " + failedChecks(checks)
		}
	}
	if err := a.sendState(ctx, update); err != nil {
		return err
	}
	if update.State == protocol.StateFailed {
		return errors.New(update.Message)
	}
	return nil
}

// runChecks checks connectivity, authentication, supported operations and
// repositories, and detects the registry version.
func (a *agent) runChecks(ctx context.Context) (checks []orbv1.CheckResult, version string, passed bool) {
	add := func(name string, err error, okMessage string) {
		c := orbv1.CheckResult{Name: name, Passed: err == nil, Message: okMessage}
		if err != nil {
			c.Message = err.Error()
		}
		checks = append(checks, c)
	}

	client, err := a.env.NewClient()
	if err != nil {
		add("connectivity", err, "")
		return checks, "", false
	}
	defer client.Close()
	modEnv := module.Env{Client: client, Credentials: a.env.Credentials}

	add("connectivity", client.Ping(ctx), "the registry answered and accepted the credentials")

	var unsupported []string
	usesCatalog := false
	for _, sc := range a.scenarios {
		for _, op := range sc.Operations() {
			if !a.env.Module.Supports(op) {
				unsupported = append(unsupported, sc.Name()+"/"+op)
			}
			usesCatalog = usesCatalog || op == orbv1.OpCatalog
		}
	}
	if len(unsupported) > 0 {
		add("operations", fmt.Errorf("not supported by the %s module: %s", a.env.Module.Name(), strings.Join(unsupported, ", ")), "")
	} else {
		add("operations", nil, "all operations are supported")
	}

	if usesCatalog {
		_, err := client.Catalog(ctx, core.StartTiming(), 1, false)
		add("catalog", err, "the account may list the catalog")
	}

	add("repositories", a.env.Module.PrepareRepositories(ctx, modEnv, a.allRepositories()), "the run can push to its repositories")

	version, err = a.env.Module.DetectVersion(ctx, modEnv)
	if err != nil {
		checks = append(checks, orbv1.CheckResult{Name: "version", Passed: true, Message: "unknown: " + err.Error()})
	} else if version != "" {
		checks = append(checks, orbv1.CheckResult{Name: "version", Passed: true, Message: version})
	}

	if a.env.Static != nil {
		checks = append(checks, orbv1.CheckResult{Name: "staticImages", Passed: true,
			Message: fmt.Sprintf("downloaded %d images, %d bytes", len(a.env.Static.Images), a.env.Static.TotalSize())})
	}

	passed = true
	for _, c := range checks {
		passed = passed && c.Passed
	}
	return checks, version, passed
}

func failedChecks(checks []orbv1.CheckResult) string {
	var failed []string
	for _, c := range checks {
		if !c.Passed {
			failed = append(failed, c.Name+": "+c.Message)
		}
	}
	return strings.Join(failed, "; ")
}

// allRepositories lists every repository of every scenario, without duplicates.
func (a *agent) allRepositories() []string {
	seen := map[string]bool{}
	var repos []string
	for _, sc := range a.scenarios {
		for _, r := range sc.Repositories() {
			if !seen[r] {
				seen[r] = true
				repos = append(repos, r)
			}
		}
	}
	return repos
}

// followCommands polls the controller and carries out its commands until
// the run is over.
func (a *agent) followCommands(ctx context.Context) error {
	seeded, cleanedUp := false, false
	done := map[int]bool{}
	failingSince := time.Time{}

	for {
		cmd, err := a.control.Command(ctx)
		if err != nil {
			if failingSince.IsZero() {
				failingSince = time.Now()
			}
			if time.Since(failingSince) > 2*time.Minute {
				return fmt.Errorf("lost contact with the controller: %w", err)
			}
			a.logf("asking for the next command: %v", err)
		} else {
			failingSince = time.Time{}
		}

		switch cmd.Action {
		case protocol.ActionSeed:
			if !seeded {
				if err := a.seed(ctx); err != nil {
					return a.fail(ctx, err)
				}
				seeded = true
				if err := a.sendState(ctx, protocol.StateUpdate{State: protocol.StateSeeded}); err != nil {
					return err
				}
			}

		case protocol.ActionRun:
			if !done[cmd.Scenario] {
				if cmd.Scenario < 0 || cmd.Scenario >= len(a.scenarios) {
					return a.fail(ctx, fmt.Errorf("the controller asked for unknown scenario %d", cmd.Scenario))
				}
				aborted, err := a.runScenario(ctx, cmd.Scenario, cmd.StartAt)
				if err != nil {
					return a.fail(ctx, err)
				}
				if aborted {
					return a.sendState(ctx, protocol.StateUpdate{State: protocol.StateAborted})
				}
				done[cmd.Scenario] = true
				if err := a.sendState(ctx, protocol.StateUpdate{State: protocol.StateScenarioDone, Scenario: cmd.Scenario}); err != nil {
					return err
				}
			}

		case protocol.ActionCleanup:
			if !cleanedUp && a.isLeader() {
				message := a.cleanup(ctx)
				cleanedUp = true
				if err := a.sendState(ctx, protocol.StateUpdate{State: protocol.StateCleanedUp, Message: message}); err != nil {
					return err
				}
			}

		case protocol.ActionAbort:
			a.logf("the run was aborted")
			return a.sendState(ctx, protocol.StateUpdate{State: protocol.StateAborted})

		case protocol.ActionExit:
			a.logf("the run is over")
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(a.opts.PollInterval):
		}
	}
}

// seed pushes this agent's share of every scenario's seed content (IMG-10).
func (a *agent) seed(ctx context.Context) error {
	for _, sc := range a.scenarios {
		a.logf("seeding scenario %s", sc.Name())
		if err := sc.Seed(ctx); err != nil {
			return fmt.Errorf("seeding scenario %q: %w", sc.Name(), err)
		}
	}
	return nil
}

// cleanup deletes everything the run pushed (CLEAN-1..4). Problems are
// reported in the message; they do not fail the run.
func (a *agent) cleanup(ctx context.Context) string {
	client, err := a.env.NewClient()
	if err != nil {
		return "cleanup failed: " + err.Error()
	}
	defer client.Close()

	message, err := a.env.Module.DeleteRepositories(ctx, module.Env{Client: client, Credentials: a.env.Credentials}, a.allRepositories())
	if err != nil {
		return "cleanup failed: " + err.Error()
	}
	a.logf("cleanup: %s", message)
	return message
}

// sendState reports a state to the controller, retrying for a while.
func (a *agent) sendState(ctx context.Context, update protocol.StateUpdate) error {
	err := retry(ctx, time.Minute, func() error { return a.control.State(ctx, update) })
	if err != nil {
		return fmt.Errorf("reporting state %s: %w", update.State, err)
	}
	return nil
}

// fail reports a fatal error to the controller and returns it.
func (a *agent) fail(ctx context.Context, err error) error {
	a.logf("fatal: %v", err)
	if a.plan != nil {
		_ = a.sendState(ctx, protocol.StateUpdate{State: protocol.StateFailed, Message: err.Error()})
	}
	return err
}
