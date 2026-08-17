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

package check

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
)

// ImagePullSecretsCheckID e' l'identificativo stabile di questa verifica.
const ImagePullSecretsCheckID = "image-pull-secrets"

// ImagePullSecrets verifica se i container che sembrano usare un registry
// diverso da Docker Hub hanno imagePullSecrets sul Pod o sul ServiceAccount.
// Il risultato resta euristico: l'hostname non rivela se il registry richiede
// davvero autenticazione.
type ImagePullSecrets struct{}

// ID implementa check.Check.
func (ImagePullSecrets) ID() string { return ImagePullSecretsCheckID }

// LooksLikePrivateRegistry applica la regola Docker/containerd che riconosce
// un registry esplicito dal primo segmento del riferimento immagine. Docker
// Hub, implicito o espresso come docker.io, e' considerato pubblico per
// default; la funzione non interroga il registry e non verifica l'accesso.
func LooksLikePrivateRegistry(image string) bool {
	first, _, found := strings.Cut(image, "/")
	if !found || first == "docker.io" {
		return false
	}
	return first == "localhost" || strings.Contains(first, ".") || strings.Contains(first, ":")
}

// Run implementa check.Check.
func (c ImagePullSecrets) Run(ctx context.Context, target Target) (Result, error) {
	var privateContainers []corev1.Container
	for _, container := range target.Workload.PodContainers() {
		if LooksLikePrivateRegistry(container.Image) {
			privateContainers = append(privateContainers, container)
		}
	}
	if len(privateContainers) == 0 || len(target.Workload.ImagePullSecretNames()) > 0 {
		return Result{CheckID: c.ID()}, nil
	}

	serviceAccountName := target.Workload.ServiceAccountName()
	if serviceAccountName == "" {
		serviceAccountName = "default"
	}
	serviceAccount, err := target.Client.CoreV1().ServiceAccounts(target.Namespace).Get(ctx, serviceAccountName, metav1.GetOptions{})
	if err != nil {
		return Skip(c.ID(), fmt.Sprintf("lettura ServiceAccount %q fallita: %v", serviceAccountName, err)), nil
	}
	if len(serviceAccount.ImagePullSecrets) > 0 {
		return Result{CheckID: c.ID()}, nil
	}

	findings := make([]model.Finding, 0, len(privateContainers))
	for _, container := range privateContainers {
		findings = append(findings, model.Finding{
			CheckID:  c.ID(),
			Severity: model.SeverityLow,
			Cause: fmt.Sprintf(
				"il container %q usa l'immagine %q su un registry che non sembra Docker Hub (dedotto esclusivamente dall'hostname, senza verificare il registry), ma ne' il Pod ne' il ServiceAccount %q dichiarano imagePullSecrets",
				container.Name, container.Image, serviceAccountName,
			),
			Evidence: []string{
				fmt.Sprintf("container=%s", container.Name),
				fmt.Sprintf("image=%s", container.Image),
				fmt.Sprintf("serviceAccount=%s", serviceAccountName),
			},
			Remediation: model.Remediation{
				Summary: fmt.Sprintf(
					"se il registry dell'immagine %q richiede autenticazione, aggiungi un imagePullSecret al pod template o al ServiceAccount %q; se il registry e' pubblico su un dominio personalizzato, questo finding e' un falso positivo perche' la deduzione usa solo l'hostname",
					container.Image, serviceAccountName,
				),
				ContextDependent: true,
			},
			Resource: model.ResourceRef{
				Kind:      "Pod",
				Namespace: target.Namespace,
				Name:      fmt.Sprintf("%s/%s", target.Workload.Name(), container.Name),
			},
		})
	}

	return Result{CheckID: c.ID(), Findings: findings}, nil
}
