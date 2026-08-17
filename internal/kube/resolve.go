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

package kube

import (
	"context"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

// ResolveWorkload interpreta un riferimento nella forma "<kind>/<name>"
// (la stessa sintassi accettata da `kubectl get`) e recupera l'oggetto
// live corrispondente. Nell'MVP e' supportato solo Deployment: gli altri
// kind restituiscono un errore esplicito invece di un comportamento
// silenziosamente parziale, cosi' chi lo usa sa che deve aspettare
// supporto futuro invece di fidarsi di un risultato incompleto.
func ResolveWorkload(ctx context.Context, clientset kubernetes.Interface, namespace, ref string) (workload.Workload, error) {
	kind, name, err := splitRef(ref)
	if err != nil {
		return nil, err
	}

	switch strings.ToLower(kind) {
	case "deployment", "deployments", "deploy", "deploy.apps", "deployment.apps":
		d, err := clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("lettura Deployment %s/%s: %w", namespace, name, err)
		}
		return workload.FromDeployment(d), nil
	default:
		return nil, fmt.Errorf("kind %q non supportato in questa versione (MVP copre solo Deployment)", kind)
	}
}

func splitRef(ref string) (kind, name string, err error) {
	parts := strings.SplitN(ref, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("riferimento %q non valido, atteso <kind>/<name> (es. deployment/api)", ref)
	}
	return parts[0], parts[1], nil
}
