// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"
)

// Default file locations inside the agent pod (see docs/architecture.md).
const (
	DefaultRegistryCredentialsFile = "/etc/orb/registry/.dockerconfigjson"
	DefaultSourceCredentialsFile   = "/etc/orb/source/.dockerconfigjson"
	DefaultCAFile                  = "/etc/orb/ca/ca.crt"
	DefaultScratchDir              = "/scratch"
)

// Options configure an agent. Run reads them from the environment; tests
// set them directly.
type Options struct {
	// ControllerURL is the base URL of the controller's agent API.
	ControllerURL string
	// RunUID and RunName identify the run.
	RunUID  string
	RunName string
	// Token authenticates the agent to the controller.
	Token string
	// AgentIndex is this agent's index (JOB_COMPLETION_INDEX).
	AgentIndex int

	// RegistryCredentialsFile, SourceCredentialsFile and CAFile are used only
	// if the files exist.
	RegistryCredentialsFile string
	SourceCredentialsFile   string
	CAFile                  string

	// ScratchDir holds the downloaded static image set.
	ScratchDir string

	// PollInterval is how often the agent asks for its next command.
	// Default 500 ms.
	PollInterval time.Duration

	// HTTPClient talks to the controller. Default: a client with a 30 s timeout.
	HTTPClient *http.Client

	// Logf receives progress messages. Default: standard error.
	Logf func(format string, args ...any)
}

// OptionsFromEnv reads the options from the environment variables described
// in docs/architecture.md.
func OptionsFromEnv() (Options, error) {
	opts := Options{
		ControllerURL: os.Getenv("ORB_CONTROLLER_URL"),
		RunUID:        os.Getenv("ORB_RUN_UID"),
		RunName:       os.Getenv("ORB_RUN_NAME"),
		Token:         os.Getenv("ORB_AGENT_TOKEN"),
	}
	var missing []string
	for name, value := range map[string]string{
		"ORB_CONTROLLER_URL": opts.ControllerURL,
		"ORB_RUN_UID":        opts.RunUID,
		"ORB_AGENT_TOKEN":    opts.Token,
	} {
		if value == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return opts, fmt.Errorf("missing environment variables: %v", missing)
	}

	index, err := strconv.Atoi(os.Getenv("JOB_COMPLETION_INDEX"))
	if err != nil {
		return opts, errors.New("JOB_COMPLETION_INDEX must be set to the agent index")
	}
	opts.AgentIndex = index
	return opts, nil
}

// withDefaults fills in unset options.
func (o Options) withDefaults() Options {
	if o.RegistryCredentialsFile == "" {
		o.RegistryCredentialsFile = DefaultRegistryCredentialsFile
	}
	if o.SourceCredentialsFile == "" {
		o.SourceCredentialsFile = DefaultSourceCredentialsFile
	}
	if o.CAFile == "" {
		o.CAFile = DefaultCAFile
	}
	if o.ScratchDir == "" {
		o.ScratchDir = DefaultScratchDir
	}
	if o.PollInterval == 0 {
		o.PollInterval = 500 * time.Millisecond
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if o.Logf == nil {
		o.Logf = func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, time.Now().Format(time.RFC3339)+" "+format+"\n", args...)
		}
	}
	return o
}

// fileExists reports whether a regular file exists at path.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
