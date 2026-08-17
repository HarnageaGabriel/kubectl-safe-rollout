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

//go:build e2e

// Package e2e_test contiene gli scenari end-to-end di `watch` contro un
// cluster kind reale (criterio di qualita' del progetto: uno scenario
// per ciascuna causa classificata). Isolato dal resto della suite con il
// build tag "e2e" cosi' `go test ./...` (CI compresa) non richiede un
// cluster: si esegue esplicitamente con `make test-e2e`, che presuppone
// un cluster kind chiamato "safe-rollout" gia' attivo (vedi README).
package e2e_test

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/diagnose"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

// defaultE2EContext e' il contesto kubeconfig atteso di convenzione in
// questo repository (`kind create cluster --name safe-rollout`).
// Sovrascrivibile con E2E_CONTEXT per chi usa un nome diverso.
const defaultE2EContext = "kind-safe-rollout"

// newE2EClient costruisce un client Kubernetes reale dal kubeconfig
// dell'utente, sul contesto del cluster kind. Fallisce il test con un
// messaggio esplicito (non uno skip silenzioso) se il contesto non
// esiste: uno scenario e2e che non gira per un cluster mancante deve
// essere visibile, non confuso con un successo.
func newE2EClient(t *testing.T) kubernetes.Interface {
	t.Helper()
	ctxName := os.Getenv("E2E_CONTEXT")
	if ctxName == "" {
		ctxName = defaultE2EContext
	}
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{CurrentContext: ctxName}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides).ClientConfig()
	if err != nil {
		t.Fatalf("configurazione per il contesto %q non disponibile: %v (serve un cluster kind: kind create cluster --name safe-rollout)", ctxName, err)
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("creazione client Kubernetes: %v", err)
	}
	return client
}

// newE2ENamespace crea un namespace usa-e-getta per isolare lo scenario
// e lo elimina a fine test, cosi' gli scenari possono girare in
// parallelo o in sequenza senza interferire tra loro.
func newE2ENamespace(t *testing.T, client kubernetes.Interface) string {
	t.Helper()
	ctx := context.Background()
	name := fmt.Sprintf("e2e-%d-%d", time.Now().UnixNano(), rand.Intn(100000))
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if _, err := client.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creazione namespace %q: %v", name, err)
	}
	t.Cleanup(func() {
		_ = client.CoreV1().Namespaces().Delete(context.Background(), name, metav1.DeleteOptions{})
	})
	return name
}

// deployWorkload crea un Deployment a singolo container con le label di
// selezione gia' coerenti tra Spec.Selector e Template, cosi' ogni
// scenario deve solo descrivere il PodSpec che produce la causa da
// classificare.
func deployWorkload(t *testing.T, client kubernetes.Interface, namespace, name string, replicas int32, podSpec corev1.PodSpec, mutate func(*appsv1.Deployment)) *appsv1.Deployment {
	t.Helper()
	labels := map[string]string{"app": name}
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       podSpec,
			},
		},
	}
	if mutate != nil {
		mutate(d)
	}
	created, err := client.AppsV1().Deployments(namespace).Create(context.Background(), d, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("creazione Deployment %s/%s: %v", namespace, name, err)
	}
	return created
}

// watchAndExpectCause esegue diagnose.Watch contro il cluster reale e
// verifica che tra i Finding emersi ce ne sia almeno uno con la CauseID
// attesa: e' la stessa funzione che alimenta `kubectl safe-rollout
// watch`, non una scorciatoia di test.
func watchAndExpectCause(t *testing.T, client kubernetes.Interface, namespace string, d *appsv1.Deployment, causeID diagnose.CauseID, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	outcome, err := diagnose.Watch(ctx, diagnose.WatchTarget{
		Namespace: namespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Watch ha restituito un errore inatteso: %v", err)
	}
	if outcome.Succeeded {
		t.Fatalf("Watch ha riportato successo, atteso il fallimento %q", causeID)
	}
	for _, res := range outcome.Results {
		for _, f := range res.Findings {
			if f.CheckID == string(causeID) {
				return
			}
		}
	}
	t.Fatalf("causa %q non trovata tra i Results: %+v", causeID, outcome.Results)
}
