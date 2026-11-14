// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

// Package modules looks up registry modules by name (REG-6). Adding a new
// registry module means adding its package and one line here.
package modules

import (
	"fmt"
	"sort"

	"github.com/zensonic/oci-reg-benchmark/internal/registry/generic"
	"github.com/zensonic/oci-reg-benchmark/internal/registry/harbor"
	"github.com/zensonic/oci-reg-benchmark/internal/registry/module"
)

var known = map[string]module.Module{
	"generic": generic.Module{},
	"harbor":  harbor.Module{},
}

// ByName returns the registry module with the given name.
func ByName(name string) (module.Module, error) {
	if name == "" {
		name = "generic"
	}
	m, ok := known[name]
	if !ok {
		return nil, fmt.Errorf("unknown registry module %q (known modules: %v)", name, Names())
	}
	return m, nil
}

// Names returns the names of all registry modules, sorted.
func Names() []string {
	names := make([]string, 0, len(known))
	for name := range known {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
