// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package export

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/zensonic/oci-reg-benchmark/internal/protocol"
	"github.com/zensonic/oci-reg-benchmark/internal/store"
)

// Register adds the read-only export endpoint to a ServeMux (RES-13).
//
// The endpoint is reached by orbctl through the Kubernetes API service
// proxy, so access is controlled by Kubernetes RBAC on services/proxy.
func Register(mux *http.ServeMux, st *store.Store) {
	mux.HandleFunc("GET "+protocol.PathExport, func(w http.ResponseWriter, r *http.Request) {
		serve(w, r, st)
	})
}

func serve(w http.ResponseWriter, r *http.Request, st *store.Store) {
	runUID := r.PathValue("uid")
	exists, err := st.HasRun(runUID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !exists {
		http.Error(w, "no detail data for run "+runUID, http.StatusNotFound)
		return
	}

	if r.URL.Query().Get("format") == "sqlite" {
		serveSQLite(w, st, runUID)
		return
	}

	detail, err := BuildDetail(st, runUID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(detail)
}

// serveSQLite streams a standalone SQLite file with only this run (RES-14).
func serveSQLite(w http.ResponseWriter, st *store.Store, runUID string) {
	dir, err := os.MkdirTemp("", "orb-export-")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer os.RemoveAll(dir)

	path := filepath.Join(dir, "run.db")
	if err := st.ExportRun(runUID, path); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	file, err := os.Open(path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer file.Close()

	w.Header().Set("Content-Type", "application/vnd.sqlite3")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.db"`, runUID))
	_, _ = io.Copy(w, file)
}
