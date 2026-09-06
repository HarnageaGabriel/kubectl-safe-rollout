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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
)

// StorageClassExistsCheckID is the stable identifier for this check.
const StorageClassExistsCheckID = "storageclass-exists"

// StorageClassExists verifies that every storageClassName a StatefulSet's
// spec.volumeClaimTemplates declares actually resolves to a real,
// cluster-scoped StorageClass object. spec.volumeClaimTemplates is a
// StatefulSet-only mechanism — the controller creates one real
// PersistentVolumeClaim per pod ordinal (e.g. data-web-0, data-web-1) from
// each template — distinct from pvc-exists's pod-template-volume PVC
// references, which name a PVC that already exists. This check closes only
// part of the gap pvc-exists documents explicitly as out of scope: it
// verifies the StorageClass reference is real, not that the per-ordinal
// PVCs themselves get created or bind. Those PVCs are dynamically named per
// ordinal and do not exist yet at check time, before the StatefulSet has any
// pods scheduled — there is nothing for this check, or any check that only
// reads live cluster state before a rollout starts, to Get for them.
//
// A nil storageClassName means "use the cluster's default StorageClass" (the
// one annotated storageclass.kubernetes.io/is-default-class: "true") — a
// legitimate, common configuration, not a misconfiguration, and not flagged.
// An explicit empty string ("") is also not flagged, but on weaker grounds
// than the equivalent unset case in priorityclass-exists/ingressclass-exists:
// the vendored doc comment on corev1.PersistentVolumeClaimSpec.StorageClassName
// (k8s.io/api/core/v1/types.go) is thin — "storageClassName is the name of
// the StorageClass required by the claim" — and does not itself spell out
// the nil-vs-empty-string distinction. The fuller contract ("" means no
// storage class, do not dynamically provision; the claim must bind to a
// pre-existing, manually-created PersistentVolume) comes from the
// documentation page that same comment links to
// (https://kubernetes.io/docs/concepts/storage/persistent-volumes#class-1),
// not from the vendored Go source itself. Stated here precisely so this
// doc comment does not claim more verification than was actually done.
//
// Only a non-nil, non-empty storageClassName that fails to resolve is a
// finding. What this check can state as verified fact, from the vendored
// type definitions alone: storageClassName is a plain string with no
// admission-time reference validation encoded in the type, so the API
// server accepts a PersistentVolumeClaim naming a StorageClass that does not
// exist — dynamic provisioning then has nothing to provision from. This
// check does not assert a specific StatefulSet-controller-side failure mode
// beyond that (e.g. whether the controller refuses to create the PVC object
// at all, or creates it and leaves it stuck Pending forever): that behavior
// lives in the controller's runtime logic, not in the vendored API types
// this module actually depends on, and was not independently verified
// against a live cluster for this check. Severity High regardless, on the
// same verified-Get, not-a-heuristic grounds already used by
// priorityclass-exists and ingressclass-exists: whatever the exact failure
// shape, a StatefulSet whose pods can never get usable storage is a real
// production risk, not a matter of degree.
//
// Applies only to StatefulSet: Deployment's spec has no volumeClaimTemplates
// field at all (appsv1.DeploymentSpec carries no such field), so
// Workload.VolumeClaimTemplates() already returns nil unconditionally for a
// Deployment-backed Target. The empty-templates guard below therefore
// covers both "this is a Deployment" and "this is a StatefulSet with no
// volumeClaimTemplates" in a single check, with zero API calls in either
// case — there is no need for a separate Kind() gate.
//
// Multiple volumeClaimTemplates are common (e.g. one for data, one for
// logs): every template is checked, one finding per template whose
// storageClassName does not resolve, not just the first. StorageClass Gets
// are memoized by name within a single Run call: two templates naming the
// same StorageClass issue one Get, not two. Memoization only avoids the
// redundant read — it does not deduplicate findings, unlike
// config-references-exist's reportedMissing map. Each volumeClaimTemplate
// is its own independent PVC-creation family (its own per-ordinal PVCs),
// so a shared missing StorageClass is still two separate, real failures,
// not one failure observed twice.
type StorageClassExists struct{}

// ID implements check.Check.
func (StorageClassExists) ID() string { return StorageClassExistsCheckID }

// Run implements check.Check.
func (c StorageClassExists) Run(ctx context.Context, target Target) (Result, error) {
	templates := target.Workload.VolumeClaimTemplates()
	if len(templates) == 0 {
		return Result{CheckID: c.ID()}, nil
	}

	workloadRef := fmt.Sprintf("%s/%s", target.Workload.Kind(), target.Workload.Name())
	resolved := make(map[string]bool, len(templates)) // storageClassName -> exists
	var findings []model.Finding
	for _, tmpl := range templates {
		name := tmpl.Spec.StorageClassName
		if name == nil || *name == "" {
			continue
		}

		exists, cached := resolved[*name]
		if !cached {
			_, err := target.Client.StorageV1().StorageClasses().Get(ctx, *name, metav1.GetOptions{})
			switch {
			case err == nil:
				exists = true
			case apierrors.IsNotFound(err):
				exists = false
			default:
				// One template's inaccessible StorageClass is not proof
				// that any other template's StorageClass is missing too,
				// but it also means this check cannot tell "exists" from
				// "does not exist" for the rest of the run: rather than
				// reporting a partial, possibly misleading set of
				// findings, the whole check degrades to Skip, the same
				// aggregation already used by ingressclass-exists.
				return Skip(c.ID(), fmt.Sprintf("failed to read StorageClass %q: %v", *name, err)), nil
			}
			resolved[*name] = exists
		}
		if exists {
			continue
		}

		findings = append(findings, model.Finding{
			CheckID:  c.ID(),
			Severity: model.SeverityHigh,
			Cause: fmt.Sprintf(
				"%s's volumeClaimTemplate %q references StorageClass %q, which does not exist: the PersistentVolumeClaim the controller creates for each pod ordinal has nothing to dynamically provision from",
				workloadRef, tmpl.Name, *name,
			),
			Evidence: []string{fmt.Sprintf("volumeClaimTemplate=%s storageClassName=%s", tmpl.Name, *name)},
			Remediation: model.Remediation{
				Summary:          fmt.Sprintf("create StorageClass %q, or correct volumeClaimTemplate %q's storageClassName if the name is wrong", *name, tmpl.Name),
				Commands:         []string{fmt.Sprintf("kubectl get storageclass %s", *name), "kubectl get storageclasses"},
				ContextDependent: true,
			},
			Resource: model.ResourceRef{Kind: "StorageClass", Name: *name},
		})
	}
	return Result{CheckID: c.ID(), Findings: findings}, nil
}
