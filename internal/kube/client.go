// Copyright 2026 Gabriel Harnagea
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package kube builds clients for the API server and resolves
// <kind>/<name> references passed on the command line into concrete
// objects. It is the only package that knows how to interact with client-go/
// cli-runtime: the rest of the program works with internal/workload.Workload
// and client-go/kubernetes.Interface passed as interfaces, so tests use
// client-go/kubernetes/fake without touching real kubeconfigs.
package kube

import (
	"fmt"

	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	metricsclientset "k8s.io/metrics/pkg/client/clientset/versioned"
)

// Clients groups the clients built from the user's kubeconfig. Metrics is nil
// when metrics-server does not respond: degrading is the caller's
// responsibility; this package must not fail.
type Clients struct {
	Config    *rest.Config
	Clientset kubernetes.Interface
	Metrics   metricsclientset.Interface
	Namespace string
}

// NewClients honors kubectl's kubeconfig, context, and standard flags through
// genericclioptions.ConfigFlags, so the plugin behaves like any other kubectl
// subcommand (--context, --namespace, etc.).
func NewClients(flags *genericclioptions.ConfigFlags) (*Clients, error) {
	restConfig, err := flags.ToRESTConfig()
	if err != nil {
		return nil, fmt.Errorf("building configuration from kubeconfig: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("creating Kubernetes client: %w", err)
	}

	namespace, _, err := flags.ToRawKubeConfigLoader().Namespace()
	if err != nil {
		return nil, fmt.Errorf("resolving namespace from kubeconfig: %w", err)
	}

	// The metrics client is optional: if metrics-server is not installed,
	// creating the client itself does not fail (it makes no network call),
	// so it is not yet possible to distinguish "absent" from "present"
	// here. Degradation happens on first use, in each check that queries it.
	metricsClient, err := metricsclientset.NewForConfig(restConfig)
	if err != nil {
		metricsClient = nil
	}

	return &Clients{
		Config:    restConfig,
		Clientset: clientset,
		Metrics:   metricsClient,
		Namespace: namespace,
	}, nil
}
