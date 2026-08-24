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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
)

// ImagePullSecretsCheckID is the stable identifier for this check.
const ImagePullSecretsCheckID = "image-pull-secrets"

// ImagePullSecrets checks whether containers that appear to use a registry
// other than Docker Hub have a usable imagePullSecret on the Pod or
// ServiceAccount. The registry judgment stays heuristic (the hostname does
// not reveal whether the registry actually requires authentication), but
// once a name is declared, this check verifies the Secret it names actually
// exists: found on kind that a Deployment can declare an imagePullSecrets
// entry that does not resolve to anything, which reads as "handled" by name
// alone while the pod sits in ImagePullBackOff. A declared-but-missing
// reference is reported with higher confidence than the pure hostname guess,
// because it is a verified fact, not an inference.
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
	if len(privateContainers) == 0 {
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

	declared := append(append([]string(nil), target.Workload.ImagePullSecretNames()...), serviceAccountSecretNames(serviceAccount)...)

	if len(declared) == 0 {
		return Result{CheckID: c.ID(), Findings: noSecretDeclaredFindings(target, privateContainers, serviceAccountName)}, nil
	}

	usable, missing, err := resolveSecrets(ctx, target, declared)
	if err != nil {
		return Skip(c.ID(), err.Error()), nil
	}
	if usable {
		return Result{CheckID: c.ID()}, nil
	}

	return Result{CheckID: c.ID(), Findings: declaredSecretMissingFindings(target, privateContainers, serviceAccountName, missing)}, nil
}

// serviceAccountSecretNames extracts the imagePullSecret names declared on a
// ServiceAccount, in the same []string shape as
// Workload.ImagePullSecretNames() so both sources merge into one list.
func serviceAccountSecretNames(sa *corev1.ServiceAccount) []string {
	names := make([]string, 0, len(sa.ImagePullSecrets))
	for _, ref := range sa.ImagePullSecrets {
		names = append(names, ref.Name)
	}
	return names
}

// resolveSecrets reports whether at least one of the named Secrets actually
// exists, and lists the ones that do not. A single working Secret is enough
// coverage: the others may be unused leftovers, not evidence of a problem.
// An error reading a Secret (typically RBAC) degrades the whole check rather
// than being silently treated as "missing", which would misreport a
// permissions gap as a broken reference.
func resolveSecrets(ctx context.Context, target Target, names []string) (usable bool, missing []string, err error) {
	for _, name := range names {
		_, getErr := target.Client.CoreV1().Secrets(target.Namespace).Get(ctx, name, metav1.GetOptions{})
		switch {
		case getErr == nil:
			usable = true
		case apierrors.IsNotFound(getErr):
			missing = append(missing, name)
		default:
			return false, nil, fmt.Errorf("failed to read Secret %q: %w", name, getErr)
		}
	}
	return usable, missing, nil
}

func noSecretDeclaredFindings(target Target, privateContainers []corev1.Container, serviceAccountName string) []model.Finding {
	findings := make([]model.Finding, 0, len(privateContainers))
	for _, container := range privateContainers {
		findings = append(findings, model.Finding{
			CheckID:  ImagePullSecretsCheckID,
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
	return findings
}

// declaredSecretMissingFindings covers the case where an imagePullSecret was
// named but does not resolve to a Secret object. Severity Medium, one step
// above the pure hostname guess above: which registry needs authentication
// is still inferred, but that the declared credential does not exist is a
// verified fact, not a guess, and it is almost always a typo or a deleted
// Secret rather than an intentional choice.
func declaredSecretMissingFindings(target Target, privateContainers []corev1.Container, serviceAccountName string, missing []string) []model.Finding {
	missingList := strings.Join(missing, ", ")
	findings := make([]model.Finding, 0, len(privateContainers))
	for _, container := range privateContainers {
		findings = append(findings, model.Finding{
			CheckID:  ImagePullSecretsCheckID,
			Severity: model.SeverityMedium,
			Cause: fmt.Sprintf(
				"container %q uses image %q from a registry that does not appear to be Docker Hub, and the declared imagePullSecret(s) (%s) do not exist in namespace %q: neither the Pod nor ServiceAccount %q has a usable credential",
				container.Name, container.Image, missingList, target.Namespace, serviceAccountName,
			),
			Evidence: []string{
				fmt.Sprintf("container=%s", container.Name),
				fmt.Sprintf("image=%s", container.Image),
				fmt.Sprintf("serviceAccount=%s", serviceAccountName),
				fmt.Sprintf("missingSecrets=%s", missingList),
			},
			Remediation: model.Remediation{
				Summary: fmt.Sprintf(
					"create the missing Secret(s) (%s) in namespace %q, or correct the imagePullSecrets reference on the Pod or ServiceAccount %q if the name is wrong",
					missingList, target.Namespace, serviceAccountName,
				),
				Commands:         []string{fmt.Sprintf("kubectl get secret %s -n %s", missing[0], target.Namespace)},
				ContextDependent: true,
			},
			Resource: model.ResourceRef{
				Kind:      "Pod",
				Namespace: target.Namespace,
				Name:      fmt.Sprintf("%s/%s", target.Workload.Name(), container.Name),
			},
		})
	}
	return findings
}
