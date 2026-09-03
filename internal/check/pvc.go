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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
)

// PVCExistsCheckID is the stable identifier for this check.
const PVCExistsCheckID = "pvc-exists"

// PVCExists verifies that every PersistentVolumeClaim a pod template
// volume names by claimName actually exists. `watch` already classifies
// this reactively as part of pending-unbound-pvc (a pod stuck Pending
// because its PVC "is not yet Bound (or does not exist)"): this check
// isolates the unambiguous, deterministic half of that — outright
// nonexistence — and catches it before the rollout ever starts, the
// same reasoning already applied to serviceaccount-exists and
// config-references-exist.
//
// Deliberately does not check binding status (the PersistentVolumeClaim's
// Phase): a freshly created PVC is routinely Pending for a moment while
// its StorageClass provisions the underlying volume, and that is not a
// problem — flagging it would risk a false positive on a perfectly
// healthy claim. Existence alone is the fact this check can state
// without guessing.
//
// Out of scope: StatefulSet's spec.volumeClaimTemplates. That is a separate,
// PVC-per-pod-per-ordinal mechanism this check does not read (it only reads
// Workload.Volumes(), i.e. the pod template's volumes) — a StatefulSet whose
// volumeClaimTemplates reference a missing StorageClass, or whose per-ordinal
// PVCs are otherwise broken, will not be flagged here.
type PVCExists struct{}

// ID implements check.Check.
func (PVCExists) ID() string { return PVCExistsCheckID }

// Run implements check.Check.
func (c PVCExists) Run(ctx context.Context, target Target) (Result, error) {
	names := pvcVolumeNames(target.Workload.Volumes())
	if len(names) == 0 {
		return Result{CheckID: c.ID()}, nil
	}

	workloadRef := fmt.Sprintf("%s/%s", target.Workload.Kind(), target.Workload.Name())
	seen := make(map[string]bool, len(names))
	var findings []model.Finding
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true

		_, err := target.Client.CoreV1().PersistentVolumeClaims(target.Namespace).Get(ctx, name, metav1.GetOptions{})
		switch {
		case err == nil:
			continue
		case apierrors.IsNotFound(err):
			findings = append(findings, model.Finding{
				CheckID:  c.ID(),
				Severity: model.SeverityHigh,
				Cause: fmt.Sprintf(
					"%s references PersistentVolumeClaim %q, which does not exist in namespace %q: the kubelet cannot mount this volume",
					workloadRef, name, target.Namespace,
				),
				Evidence: []string{fmt.Sprintf("persistentVolumeClaim=%s", name)},
				Remediation: model.Remediation{
					Summary:          fmt.Sprintf("create PersistentVolumeClaim %q in namespace %q, or correct the claimName referenced in the pod template", name, target.Namespace),
					Commands:         []string{fmt.Sprintf("kubectl get pvc %s -n %s", name, target.Namespace)},
					ContextDependent: true,
				},
				Resource: model.ResourceRef{Kind: "PersistentVolumeClaim", Namespace: target.Namespace, Name: name},
			})
		default:
			return Skip(c.ID(), fmt.Sprintf("failed to read PersistentVolumeClaim %q: %v", name, err)), nil
		}
	}
	return Result{CheckID: c.ID(), Findings: findings}, nil
}

func pvcVolumeNames(volumes []corev1.Volume) []string {
	var names []string
	for _, v := range volumes {
		if v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName != "" {
			names = append(names, v.PersistentVolumeClaim.ClaimName)
		}
	}
	return names
}
