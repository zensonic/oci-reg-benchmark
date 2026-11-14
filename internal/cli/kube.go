// Copyright The ORB Authors
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	orbv1 "github.com/zensonic/oci-reg-benchmark/api/v1alpha1"
	"github.com/zensonic/oci-reg-benchmark/internal/protocol"
)

// Controller Service that serves the export endpoint (RES-13).
const (
	controllerService = "orb-controller"
	controllerPort    = "8082"
)

// kube is orbctl's connection to the cluster.
type kube struct {
	// client reads and writes ORB resources.
	client client.Client

	// namespace is where benchmarks and runs are looked up.
	namespace string

	// fetchDetail downloads a run's detail data from the controller's export
	// endpoint, as JSON or, if sqlite is true, as a SQLite file.
	fetchDetail func(ctx context.Context, runUID string, sqlite bool) ([]byte, error)
}

// newScheme returns a scheme with the Kubernetes core types and ORB's types.
func newScheme() (*runtime.Scheme, error) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, err
	}
	if err := orbv1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	return scheme, nil
}

// connectToCluster loads the kubeconfig and connects to the cluster.
func connectToCluster(o *options) (*kube, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if o.kubeconfig != "" {
		rules.ExplicitPath = o.kubeconfig
	}
	overrides := &clientcmd.ConfigOverrides{CurrentContext: o.kubeContext}
	config := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides)

	restConfig, err := config.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("loading kubeconfig: %w", err)
	}

	namespace := o.namespace
	if namespace == "" {
		namespace, _, err = config.Namespace()
		if err != nil {
			return nil, fmt.Errorf("reading the namespace from the kubeconfig: %w", err)
		}
	}

	scheme, err := newScheme()
	if err != nil {
		return nil, err
	}
	c, err := client.New(restConfig, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("connecting to the cluster: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("connecting to the cluster: %w", err)
	}

	controllerNamespace := o.controllerNamespace
	return &kube{
		client:    c,
		namespace: namespace,
		fetchDetail: func(ctx context.Context, runUID string, sqlite bool) ([]byte, error) {
			params := map[string]string{}
			if sqlite {
				params["format"] = "sqlite"
			}
			// The Kubernetes API service proxy keeps access under RBAC:
			// users need "get" on services/proxy for orb-controller.
			data, err := clientset.CoreV1().Services(controllerNamespace).
				ProxyGet("http", controllerService, controllerPort, protocol.ExportPath(runUID), params).
				DoRaw(ctx)
			if err != nil {
				return nil, fmt.Errorf("downloading detail data from the ORB controller in namespace %s: %w", controllerNamespace, err)
			}
			return data, nil
		},
	}, nil
}
