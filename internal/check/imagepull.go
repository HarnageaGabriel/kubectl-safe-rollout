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

// ImagePullSecretsCheckID is the stable identifier for this check.
const ImagePullSecretsCheckID = "image-pull-secrets"

// ImagePullSecrets checks whether containers that appear to use a registry
// other than Docker Hub have imagePullSecrets on the Pod or ServiceAccount.
// The result remains heuristic: the hostname does not reveal whether the
// registry actually requires authentication.
type ImagePullSecrets struct{}

// ID implements check.Check.
func (ImagePullSecrets) ID() string { return ImagePullSecretsCheckID }

// LooksLikePrivateRegistry applies the Docker/containerd rule that identifies
// an explicit registry from the first segment of the image reference. Docker
// Hub, implicit or expressed as docker.io, is considered public by default;
// the function does not query the registry or verify access.
func LooksLikePrivateRegistry(image string) bool {
	first, _, found := strings.Cut(image, "/")
	if !found || first == "docker.io" {
		return false
	}
	return first == "localhost" || strings.Contains(first, ".") || strings.Contains(first, ":")
}

// Run implements check.Check.
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
		return Skip(c.ID(), fmt.Sprintf("failed to read ServiceAccount %q: %v", serviceAccountName, err)), nil
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
				"container %q uses image %q from a registry that does not appear to be Docker Hub (inferred exclusively from the hostname, without checking the registry), but neither the Pod nor ServiceAccount %q declares imagePullSecrets",
				container.Name, container.Image, serviceAccountName,
			),
			Evidence: []string{
				fmt.Sprintf("container=%s", container.Name),
				fmt.Sprintf("image=%s", container.Image),
				fmt.Sprintf("serviceAccount=%s", serviceAccountName),
			},
			Remediation: model.Remediation{
				Summary: fmt.Sprintf(
					"if the registry for image %q requires authentication, add an imagePullSecret to the pod template or ServiceAccount %q; if the registry is public on a custom domain, this finding is a false positive because the inference uses only the hostname",
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
