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

// Package kube costruisce i client verso l'API server e risolve i
// riferimenti <kind>/<name> passati sulla riga di comando in oggetti
// concreti. E' l'unico package che sa come parlare a client-go/
// cli-runtime: il resto del programma lavora su internal/workload.Workload
// e su client-go/kubernetes.Interface passati per interfaccia, cosi' i
// test usano client-go/kubernetes/fake senza toccare kubeconfig reali.
package kube

import (
	"fmt"

	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	metricsclientset "k8s.io/metrics/pkg/client/clientset/versioned"
)

// Clients raggruppa i client costruiti a partire dal kubeconfig
// dell'utente. Metrics e' nil quando metrics-server non risponde: e'
// responsabilita' del chiamante degradare, non di questo package fallire.
type Clients struct {
	Config    *rest.Config
	Clientset kubernetes.Interface
	Metrics   metricsclientset.Interface
	Namespace string
}

// NewClients rispetta kubeconfig, context e flag standard di kubectl
// tramite genericclioptions.ConfigFlags, cosi' il plugin si comporta come
// ogni altro sottocomando di kubectl (--context, --namespace, ecc.).
func NewClients(flags *genericclioptions.ConfigFlags) (*Clients, error) {
	restConfig, err := flags.ToRESTConfig()
	if err != nil {
		return nil, fmt.Errorf("costruzione configurazione da kubeconfig: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("creazione client Kubernetes: %w", err)
	}

	namespace, _, err := flags.ToRawKubeConfigLoader().Namespace()
	if err != nil {
		return nil, fmt.Errorf("risoluzione namespace da kubeconfig: %w", err)
	}

	// Il client metrics e' opzionale: se metrics-server non e'
	// installato la creazione del client stesso non fallisce (non fa
	// una chiamata di rete), quindi qui non e' ancora possibile
	// distinguere "assente" da "presente". La degradazione avviene al
	// primo uso, nelle singole verifiche che lo consultano.
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
