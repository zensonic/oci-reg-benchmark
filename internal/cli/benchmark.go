// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	orbv1 "github.com/zensonic/oci-reg-benchmark/api/v1alpha1"
)

func newBenchmarkCommand(o *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "benchmark",
		Aliases: []string{"benchmarks", "bench"},
		Short:   "Manage benchmark definitions (OrbBenchmark)",
	}
	cmd.AddCommand(
		newBenchmarkApplyCommand(o),
		newBenchmarkListCommand(o),
		newBenchmarkGetCommand(o),
		newBenchmarkDeleteCommand(o),
	)
	return cmd
}

func newBenchmarkApplyCommand(o *options) *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "apply -f FILE",
		Short: "Create or update benchmarks from a YAML file",
		Long: "Create or update every OrbBenchmark in a YAML file. The file may hold several\n" +
			"documents; documents of other kinds, such as Secrets, are skipped.\nUse -f - to read from standard input.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			data, err := readInput(file)
			if err != nil {
				return err
			}
			benchmarks, skipped, err := decodeBenchmarks(data)
			if err != nil {
				return err
			}
			for _, kind := range skipped {
				fmt.Fprintf(o.errOut, "skipping a document of kind %s: orbctl benchmark apply only applies OrbBenchmark\n", kind)
			}
			if len(benchmarks) == 0 {
				return errors.New("the file contains no OrbBenchmark")
			}

			k, err := o.client()
			if err != nil {
				return err
			}
			for i := range benchmarks {
				action, err := applyBenchmark(cmd, k, &benchmarks[i])
				if err != nil {
					return err
				}
				fmt.Fprintf(o.out, "orbbenchmark/%s %s\n", benchmarks[i].Name, action)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "filename", "f", "", "YAML file with OrbBenchmark definitions, or - for standard input")
	_ = cmd.MarkFlagRequired("filename")
	return cmd
}

func readInput(file string) ([]byte, error) {
	if file == "-" {
		return io.ReadAll(os.Stdin)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", file, err)
	}
	return data, nil
}

// decodeBenchmarks splits YAML into documents and decodes every OrbBenchmark
// strictly, so typos in field names are reported. It returns the kinds of
// skipped documents.
func decodeBenchmarks(data []byte) ([]orbv1.OrbBenchmark, []string, error) {
	reader := k8syaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(data)))
	var benchmarks []orbv1.OrbBenchmark
	var skipped []string

	for {
		doc, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("reading YAML: %w", err)
		}
		if len(bytes.TrimSpace(doc)) == 0 {
			continue
		}

		var meta struct {
			Kind string `json:"kind"`
		}
		if err := yaml.Unmarshal(doc, &meta); err != nil {
			return nil, nil, fmt.Errorf("parsing YAML: %w", err)
		}
		if meta.Kind == "" && len(bytes.TrimSpace(stripComments(doc))) == 0 {
			continue // a document with only comments
		}
		if meta.Kind != "OrbBenchmark" {
			skipped = append(skipped, orDash(meta.Kind))
			continue
		}

		var b orbv1.OrbBenchmark
		if err := yaml.UnmarshalStrict(doc, &b); err != nil {
			return nil, nil, fmt.Errorf("decoding OrbBenchmark: %w", err)
		}
		if b.Name == "" {
			return nil, nil, errors.New("an OrbBenchmark has no metadata.name")
		}
		benchmarks = append(benchmarks, b)
	}
	return benchmarks, skipped, nil
}

func stripComments(doc []byte) []byte {
	var out []string
	for _, line := range strings.Split(string(doc), "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			out = append(out, line)
		}
	}
	return []byte(strings.Join(out, "\n"))
}

// applyBenchmark creates the benchmark, or replaces the spec of an existing one.
func applyBenchmark(cmd *cobra.Command, k *kube, b *orbv1.OrbBenchmark) (string, error) {
	ctx := cmd.Context()
	if b.Namespace == "" {
		b.Namespace = k.namespace
	}

	// Check the spec locally first so mistakes are reported clearly.
	check := b.Spec.DeepCopy()
	check.Default()
	if err := check.Validate(); err != nil {
		return "", fmt.Errorf("orbbenchmark/%s is not valid: %w", b.Name, err)
	}

	var existing orbv1.OrbBenchmark
	err := k.client.Get(ctx, client.ObjectKeyFromObject(b), &existing)
	if apierrors.IsNotFound(err) {
		if err := k.client.Create(ctx, b); err != nil {
			return "", fmt.Errorf("creating orbbenchmark/%s: %w", b.Name, err)
		}
		return "created", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading orbbenchmark/%s: %w", b.Name, err)
	}

	existing.Spec = b.Spec
	existing.Labels = mergeMaps(existing.Labels, b.Labels)
	existing.Annotations = mergeMaps(existing.Annotations, b.Annotations)
	if err := k.client.Update(ctx, &existing); err != nil {
		return "", fmt.Errorf("updating orbbenchmark/%s: %w", b.Name, err)
	}
	return "configured", nil
}

func mergeMaps(base, extra map[string]string) map[string]string {
	if len(extra) == 0 {
		return base
	}
	if base == nil {
		base = map[string]string{}
	}
	for k, v := range extra {
		base[k] = v
	}
	return base
}

func newBenchmarkListCommand(o *options) *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List benchmarks",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := checkFormat(format, formatTable, formatJSON, formatYAML); err != nil {
				return err
			}
			k, err := o.client()
			if err != nil {
				return err
			}
			var list orbv1.OrbBenchmarkList
			if err := k.client.List(cmd.Context(), &list, client.InNamespace(k.namespace)); err != nil {
				return fmt.Errorf("listing benchmarks: %w", err)
			}
			if format != formatTable {
				return printStructured(o.out, format, list)
			}
			if len(list.Items) == 0 {
				fmt.Fprintf(o.out, "No benchmarks in namespace %s.\n", k.namespace)
				return nil
			}
			t := newTable(o.out, "NAME", "REGISTRY", "MODULE", "IMAGE SET", "SCENARIOS", "AGE")
			for _, b := range list.Items {
				t.row(b.Name, b.Spec.Registry.URL, orDash(b.Spec.Registry.Module), orDash(string(b.Spec.ImageSet.Type)),
					scenarioNames(b.Spec.Scenarios), age(b.CreationTimestamp.Time))
			}
			return t.flush()
		},
	}
	cmd.Flags().StringVarP(&format, "output", "o", formatTable, "output format: table, json or yaml")
	return cmd
}

func scenarioNames(scenarios []orbv1.ScenarioSpec) string {
	names := make([]string, len(scenarios))
	for i, s := range scenarios {
		names[i] = fmt.Sprintf("%s(%s)", s.Name, s.Type)
	}
	return orDash(strings.Join(names, ","))
}

func newBenchmarkGetCommand(o *options) *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "get NAME",
		Short: "Show a benchmark",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkFormat(format, formatTable, formatJSON, formatYAML); err != nil {
				return err
			}
			k, err := o.client()
			if err != nil {
				return err
			}
			var b orbv1.OrbBenchmark
			if err := k.client.Get(cmd.Context(), client.ObjectKey{Namespace: k.namespace, Name: args[0]}, &b); err != nil {
				return fmt.Errorf("reading orbbenchmark/%s: %w", args[0], err)
			}
			if format != formatTable {
				b.ManagedFields = nil
				return printStructured(o.out, format, b)
			}
			return printBenchmark(o.out, &b)
		},
	}
	cmd.Flags().StringVarP(&format, "output", "o", formatTable, "output format: table, json or yaml")
	return cmd
}

func printBenchmark(w io.Writer, b *orbv1.OrbBenchmark) error {
	s := b.Spec
	fmt.Fprintf(w, "Name:        %s\n", b.Name)
	fmt.Fprintf(w, "Namespace:   %s\n", b.Namespace)
	fmt.Fprintf(w, "Registry:    %s (module %s)\n", s.Registry.URL, orDash(s.Registry.Module))
	fmt.Fprintf(w, "Repository:  %s/<run name>\n", s.Registry.RepositoryPrefix)
	fmt.Fprintf(w, "Image set:   %s\n", orDash(string(s.ImageSet.Type)))
	fmt.Fprintf(w, "Cleanup:     %s\n", orDash(string(s.Cleanup)))
	fmt.Fprintln(w)

	t := newTable(w, "SCENARIO", "TYPE", "STAGES", "PEAK WORKERS")
	for _, sc := range s.Scenarios {
		peak := 0
		stages := make([]string, len(sc.Profile.Stages))
		for i, st := range sc.Profile.Stages {
			stages[i] = fmt.Sprintf("%s:%d", st.Duration.Duration, st.Workers)
			peak = max(peak, st.Workers)
		}
		t.row(sc.Name, sc.Type, strings.Join(stages, ","), peak)
	}
	return t.flush()
}

func newBenchmarkDeleteCommand(o *options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete NAME",
		Short: "Delete a benchmark (earlier runs and results are kept)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, err := o.client()
			if err != nil {
				return err
			}
			b := &orbv1.OrbBenchmark{}
			b.Namespace, b.Name = k.namespace, args[0]
			if err := k.client.Delete(cmd.Context(), b); err != nil {
				return fmt.Errorf("deleting orbbenchmark/%s: %w", args[0], err)
			}
			fmt.Fprintf(o.out, "orbbenchmark/%s deleted\n", args[0])
			return nil
		},
	}
}
