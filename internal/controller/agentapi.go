// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-logr/logr"

	"github.com/zensonic/oci-reg-benchmark/internal/export"
	"github.com/zensonic/oci-reg-benchmark/internal/protocol"
	"github.com/zensonic/oci-reg-benchmark/internal/store"
)

// maxReportSize limits the size of one interval report.
const maxReportSize = 64 << 20

// agentAPI serves the HTTP API the agents use (see internal/protocol).
type agentAPI struct {
	coordinator *Coordinator
	log         logr.Logger
}

// newAPIHandler returns the handler for the agent API and the export
// endpoint, which share one listener.
func newAPIHandler(coordinator *Coordinator, st *store.Store, log logr.Logger) http.Handler {
	api := &agentAPI{coordinator: coordinator, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+protocol.PathPlan, api.withRun(api.plan))
	mux.HandleFunc("GET "+protocol.PathCommand, api.withAgent(api.command))
	mux.HandleFunc("POST "+protocol.PathState, api.withAgent(api.state))
	mux.HandleFunc("POST "+protocol.PathReport, api.withAgent(api.report))
	if st != nil {
		export.Register(mux, st)
	}
	return mux
}

// withRun looks up the run and checks the agent's bearer token.
func (a *agentAPI) withRun(next func(http.ResponseWriter, *http.Request, *RunState)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rs, ok := a.coordinator.Get(r.PathValue("uid"))
		if !ok {
			// Unknown or forgotten run: tell the agent it is over.
			http.Error(w, "unknown run", http.StatusNotFound)
			return
		}
		token, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !found || subtle.ConstantTimeCompare([]byte(token), []byte(rs.Token)) != 1 {
			http.Error(w, "invalid agent token", http.StatusUnauthorized)
			return
		}
		next(w, r, rs)
	}
}

// withAgent also parses and checks the agent index.
func (a *agentAPI) withAgent(next func(http.ResponseWriter, *http.Request, *RunState, int)) http.HandlerFunc {
	return a.withRun(func(w http.ResponseWriter, r *http.Request, rs *RunState) {
		agent, err := strconv.Atoi(r.PathValue("agent"))
		if err != nil || agent < 0 || agent >= rs.Plan.Agents {
			http.Error(w, "invalid agent index", http.StatusBadRequest)
			return
		}
		next(w, r, rs, agent)
	})
}

func (a *agentAPI) plan(w http.ResponseWriter, _ *http.Request, rs *RunState) {
	writeJSON(w, rs.Plan)
}

func (a *agentAPI) command(w http.ResponseWriter, _ *http.Request, rs *RunState, agent int) {
	writeJSON(w, rs.Command(agent))
}

func (a *agentAPI) state(w http.ResponseWriter, r *http.Request, rs *RunState, agent int) {
	var update protocol.StateUpdate
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&update); err != nil {
		http.Error(w, "invalid state update: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := rs.Update(agent, update); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	a.log.Info("agent state", "run", rs.Plan.RunName, "agent", agent, "state", update.State, "message", update.Message)
	w.WriteHeader(http.StatusNoContent)
}

func (a *agentAPI) report(w http.ResponseWriter, r *http.Request, rs *RunState, agent int) {
	var report protocol.IntervalReport
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxReportSize)).Decode(&report); err != nil {
		http.Error(w, "invalid report: "+err.Error(), http.StatusBadRequest)
		return
	}
	aggregator := rs.Aggregator()
	if aggregator == nil {
		// Results are already written; late reports are dropped.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := aggregator.AddReport(agent, report); err != nil {
		a.log.Error(err, "storing report", "run", rs.Plan.RunName, "agent", agent)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
