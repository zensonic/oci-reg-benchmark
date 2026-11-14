// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"sigs.k8s.io/yaml"
)

// Output formats (CLI-4).
const (
	formatTable = "table"
	formatJSON  = "json"
	formatYAML  = "yaml"
)

// checkFormat returns an error if format is not one of allowed.
func checkFormat(format string, allowed ...string) error {
	for _, a := range allowed {
		if format == a {
			return nil
		}
	}
	return fmt.Errorf("unknown output format %q (allowed: %s)", format, strings.Join(allowed, ", "))
}

// printStructured prints v as JSON or YAML.
func printStructured(w io.Writer, format string, v any) error {
	switch format {
	case formatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	case formatYAML:
		data, err := yaml.Marshal(v)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	default:
		return fmt.Errorf("unsupported structured format %q", format)
	}
}

// table writes aligned columns.
type table struct {
	tw *tabwriter.Writer
}

func newTable(w io.Writer, headers ...string) *table {
	t := &table{tw: tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)}
	if len(headers) > 0 {
		t.row(toAny(headers)...)
	}
	return t
}

// row writes one row; values are formatted with fmt.Sprint.
func (t *table) row(values ...any) {
	cells := make([]string, len(values))
	for i, v := range values {
		cells[i] = fmt.Sprint(v)
	}
	fmt.Fprintln(t.tw, strings.Join(cells, "\t"))
}

func (t *table) flush() error {
	return t.tw.Flush()
}

func toAny(values []string) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = v
	}
	return out
}

// millis formats a millisecond value.
func millis(v float64) string {
	return fmt.Sprintf("%.2f", v)
}

// rate formats operations per second.
func rate(v float64) string {
	return fmt.Sprintf("%.1f", v)
}

// megabytes formats bytes per second as MB/s, or "-" when there is no data.
func megabytes(bytesPerSecond float64) string {
	if bytesPerSecond <= 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f", bytesPerSecond/1e6)
}

// percentChange formats the change from a to b, for example "+12.5%".
func percentChange(a, b float64) string {
	if a == 0 {
		if b == 0 {
			return "0%"
		}
		return "n/a"
	}
	return fmt.Sprintf("%+.1f%%", (b-a)/a*100)
}

// age formats how long ago t was, like kubectl does.
func age(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t).Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// orDash returns s, or "-" if s is empty.
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
