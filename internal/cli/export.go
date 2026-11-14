// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/spf13/cobra"

	orbv1 "github.com/zensonic/oci-reg-benchmark/api/v1alpha1"
	"github.com/zensonic/oci-reg-benchmark/internal/protocol"
)

// exportDocument is the JSON export: the summary from the OrbBenchmarkResult
// and the detail data from SQLite, in one document (RES-14).
type exportDocument struct {
	Summary orbv1.ResultSummary `json:"summary"`
	Detail  protocol.Detail     `json:"detail"`
}

func newResultExportCommand(o *options) *cobra.Command {
	var (
		format string
		output string
	)
	cmd := &cobra.Command{
		Use:   "export RUN -o json|csv|sqlite --output PATH",
		Short: "Export the summary and detail data of a run",
		Long: "Export a run's results: the summary from the OrbBenchmarkResult together with the\n" +
			"full-resolution detail data stored by the ORB controller.\n\n" +
			"  json    one file with \"summary\" and \"detail\"\n" +
			"  csv     a directory with one CSV file per table\n" +
			"  sqlite  a standalone SQLite database with only this run\n\n" +
			"The detail data is downloaded through the Kubernetes API service proxy, so you need\n" +
			"\"get\" permission on services/proxy for orb-controller in the controller namespace.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkFormat(format, formatJSON, "csv", "sqlite"); err != nil {
				return err
			}
			if output == "" {
				return fmt.Errorf("--output is required")
			}
			k, err := o.client()
			if err != nil {
				return err
			}
			result, err := getResult(cmd, k, args[0])
			if err != nil {
				return err
			}
			runUID := result.Summary.Detail.RunUID
			if runUID == "" {
				runUID = result.Summary.RunUID
			}

			if format == "sqlite" {
				data, err := k.fetchDetail(cmd.Context(), runUID, true)
				if err != nil {
					return err
				}
				if err := os.WriteFile(output, data, 0o600); err != nil {
					return fmt.Errorf("writing %s: %w", output, err)
				}
				fmt.Fprintf(o.errOut, "wrote %s\n", output)
				return nil
			}

			data, err := k.fetchDetail(cmd.Context(), runUID, false)
			if err != nil {
				return err
			}
			var detail protocol.Detail
			if err := json.Unmarshal(data, &detail); err != nil {
				return fmt.Errorf("decoding the detail data from the controller: %w", err)
			}

			if format == formatJSON {
				if err := writeJSONExport(output, result.Summary, detail); err != nil {
					return err
				}
			} else {
				if err := writeCSVExport(output, result.Summary, detail); err != nil {
					return err
				}
			}
			fmt.Fprintf(o.errOut, "wrote %s\n", output)
			return nil
		},
	}
	cmd.Flags().StringVarP(&format, "output-format", "o", formatJSON, "export format: json, csv or sqlite")
	cmd.Flags().StringVar(&output, "output", "", "file (json, sqlite) or directory (csv) to write; - writes JSON to standard output")
	return cmd
}

// writeJSONExport writes the combined JSON document to path, or to standard
// output if path is "-".
func writeJSONExport(path string, summary orbv1.ResultSummary, detail protocol.Detail) error {
	doc := exportDocument{Summary: summary, Detail: detail}
	if path == "-" {
		return encodeIndented(os.Stdout, doc)
	}

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("creating %s: %w", path, err)
	}
	if err := encodeIndented(f, doc); err != nil {
		_ = f.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	// Close can report a failed write, so its error matters here.
	if err := f.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

func encodeIndented(w io.Writer, value any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}

// csvTable is one CSV file of the export.
type csvTable struct {
	file string
	rows [][]string
}

// buildCSVTables turns a summary and its detail data into CSV tables. The
// samples table is only included when samples were recorded.
func buildCSVTables(summary orbv1.ResultSummary, detail protocol.Detail) []csvTable {
	operations := csvTable{file: "summary_operations.csv", rows: [][]string{{
		"scenario", "period", "operation", "count", "errors", "ops_per_second",
		"latency_min_ms", "latency_p50_ms", "latency_p90_ms", "latency_p95_ms", "latency_p99_ms", "latency_p999_ms", "latency_max_ms", "latency_mean_ms",
		"total_bytes", "aggregate_bytes_per_second",
	}}}
	for _, sc := range summary.Scenarios {
		appendOperationRows(&operations, sc.Name, "measured", sc.Operations)
		appendOperationRows(&operations, sc.Name, "warmup", sc.Warmup)
	}

	intervals := csvTable{file: "intervals.csv", rows: [][]string{{
		"scenario", "interval", "stage", "warmup", "workers", "operation", "count", "errors", "bytes",
		"p50_ms", "p90_ms", "p99_ms", "max_ms",
	}}}
	phases := csvTable{file: "phases.csv", rows: [][]string{{
		"scenario", "operation", "metric", "count", "min_ms", "p50_ms", "p90_ms", "p95_ms", "p99_ms", "p999_ms", "max_ms", "mean_ms",
	}}}
	agents := csvTable{file: "agents.csv", rows: [][]string{{
		"scenario", "agent", "interval", "workers", "cpu_utilization", "sched_delay_p99_ms",
	}}}
	errorsTable := csvTable{file: "errors.csv", rows: [][]string{{
		"scenario", "operation", "type", "count", "example",
	}}}
	samples := csvTable{file: "samples.csv", rows: [][]string{{
		"scenario", "agent", "operation", "offset_us", "latency_us", "total_us", "bytes", "error",
	}}}

	for _, sc := range detail.Scenarios {
		for _, iv := range sc.Intervals {
			for _, op := range iv.Operations {
				intervals.rows = append(intervals.rows, []string{
					sc.Name, itoa(iv.Interval), itoa(iv.Stage), strconv.FormatBool(iv.Warmup), itoa(iv.Workers),
					op.Operation, i64(op.Count), i64(op.Errors), i64(op.Bytes),
					f64(op.P50Millis), f64(op.P90Millis), f64(op.P99Millis), f64(op.MaxMillis),
				})
			}
		}
		for _, p := range sc.Phases {
			l := p.Latency
			phases.rows = append(phases.rows, []string{
				sc.Name, p.Operation, p.Metric, i64(p.Count), f64(l.MinMillis), f64(l.P50Millis), f64(l.P90Millis),
				f64(l.P95Millis), f64(l.P99Millis), f64(l.P999Millis), f64(l.MaxMillis), f64(l.MeanMillis),
			})
		}
		for _, a := range sc.Agents {
			agents.rows = append(agents.rows, []string{
				sc.Name, itoa(a.Agent), itoa(a.Interval), itoa(a.Workers), f64(a.CPUUtilization), f64(a.SchedDelayP99Millis),
			})
		}
		for _, e := range sc.Errors {
			errorsTable.rows = append(errorsTable.rows, []string{sc.Name, e.Operation, e.Type, i64(e.Count), e.Example})
		}
		for _, s := range sc.Samples {
			samples.rows = append(samples.rows, []string{
				sc.Name, itoa(s.Agent), s.Operation, i64(s.OffsetMicros), i64(s.LatencyMicros), i64(s.TotalMicros), i64(s.Bytes), s.Error,
			})
		}
	}

	tables := []csvTable{operations, intervals, phases, agents, errorsTable}
	if len(samples.rows) > 1 {
		tables = append(tables, samples)
	}
	return tables
}

func appendOperationRows(t *csvTable, scenario, period string, ops []orbv1.OperationResult) {
	for _, op := range ops {
		l := op.Latency
		totalBytes, aggregate := int64(0), 0.0
		if op.Throughput != nil {
			totalBytes, aggregate = op.Throughput.TotalBytes, op.Throughput.AggregateBytesPerSecond
		}
		t.rows = append(t.rows, []string{
			scenario, period, op.Operation, i64(op.Count), i64(op.Errors), f64(op.OpsPerSecond),
			f64(l.MinMillis), f64(l.P50Millis), f64(l.P90Millis), f64(l.P95Millis), f64(l.P99Millis), f64(l.P999Millis),
			f64(l.MaxMillis), f64(l.MeanMillis), i64(totalBytes), f64(aggregate),
		})
	}
}

// writeCSVExport writes the CSV tables into a directory, creating it if needed.
func writeCSVExport(dir string, summary orbv1.ResultSummary, detail protocol.Detail) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	for _, table := range buildCSVTables(summary, detail) {
		path := filepath.Join(dir, table.file)
		f, err := os.Create(path)
		if err != nil {
			return fmt.Errorf("creating %s: %w", path, err)
		}
		w := csv.NewWriter(f)
		if err := w.WriteAll(table.rows); err != nil {
			_ = f.Close()
			return fmt.Errorf("writing %s: %w", path, err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("writing %s: %w", path, err)
		}
	}
	return nil
}

func itoa(v int) string    { return strconv.Itoa(v) }
func i64(v int64) string   { return strconv.FormatInt(v, 10) }
func f64(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
