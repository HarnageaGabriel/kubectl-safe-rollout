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

package workload

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
)

type statefulSetWorkload struct {
	s *appsv1.StatefulSet
}

// FromStatefulSet builds a Workload from a live StatefulSet.
func FromStatefulSet(s *appsv1.StatefulSet) Workload {
	return &statefulSetWorkload{s: s}
}

func (w *statefulSetWorkload) Kind() string      { return "StatefulSet" }
func (w *statefulSetWorkload) Name() string      { return w.s.Name }
func (w *statefulSetWorkload) Namespace() string { return w.s.Namespace }
func (w *statefulSetWorkload) UID() types.UID    { return w.s.UID }

func (w *statefulSetWorkload) Replicas() int32 {
	if w.s.Spec.Replicas == nil {
		return 1
	}
	return *w.s.Spec.Replicas
}

func (w *statefulSetWorkload) PodLabels() map[string]string {
	return w.s.Spec.Template.Labels
}

func (w *statefulSetWorkload) PodSelector() (labels.Selector, error) {
	return metav1.LabelSelectorAsSelector(w.s.Spec.Selector)
}

// UpdateStrategy normalizes StatefulSet's update strategy into the shared
// UpdateStrategy shape. Unlike Deployment, StatefulSet has no MaxSurge field
// at all (a rolling update replaces one pod at a time, deleting the old one
// before creating its replacement, so it never exceeds Replicas): MaxSurge is
// always nil here, not defaulted, so surgeCount-style callers correctly treat
// StatefulSet as never surging. MaxUnavailable does exist on
// RollingUpdateStatefulSetStrategy (gated by the MaxUnavailableStatefulSet
// feature gate, beta and enabled by default as of the k8s.io/api version this
// module depends on) but, unlike Deployment's MaxUnavailable, Kubernetes does
// not guarantee server-side defaulting to a fixed value in every version:
// this method populates it only when the RollingUpdate struct sets it
// explicitly, leaving it nil (not "1") when absent, to avoid asserting a
// default this project has not verified against every supported server
// version.
func (w *statefulSetWorkload) UpdateStrategy() UpdateStrategy {
	strategyType := w.s.Spec.UpdateStrategy.Type
	if strategyType == "" {
		strategyType = RollingUpdate
	}
	if strategyType == appsv1.OnDeleteStatefulSetStrategyType {
		return UpdateStrategy{Type: OnDelete}
	}

	ru := w.s.Spec.UpdateStrategy.RollingUpdate
	strategy := UpdateStrategy{Type: RollingUpdate}
	if ru != nil {
		if ru.MaxUnavailable != nil {
			maxUnavailable := *ru.MaxUnavailable
			strategy.MaxUnavailable = &maxUnavailable
		}
		if ru.Partition != nil {
			partition := *ru.Partition
			strategy.Partition = &partition
		}
	}
	return strategy
}

// PodRequests implements Workload.
func (w *statefulSetWorkload) PodRequests() corev1.ResourceList {
	total := corev1.ResourceList{}
	for _, c := range w.s.Spec.Template.Spec.Containers {
		for name, qty := range c.Resources.Requests {
			sum := total[name]
			sum.Add(qty)
			total[name] = sum
		}
	}
	return total
}

// PodContainers implements Workload.
func (w *statefulSetWorkload) PodContainers() []corev1.Container {
	return w.s.Spec.Template.Spec.Containers
}

// InitContainers implements Workload.
func (w *statefulSetWorkload) InitContainers() []corev1.Container {
	return w.s.Spec.Template.Spec.InitContainers
}

// Volumes implements Workload.
func (w *statefulSetWorkload) Volumes() []corev1.Volume {
	return w.s.Spec.Template.Spec.Volumes
}

// ImagePullSecretNames implements Workload.
func (w *statefulSetWorkload) ImagePullSecretNames() []string {
	refs := w.s.Spec.Template.Spec.ImagePullSecrets
	names := make([]string, 0, len(refs))
	for _, ref := range refs {
		names = append(names, ref.Name)
	}
	return names
}

// ServiceAccountName implements Workload.
func (w *statefulSetWorkload) ServiceAccountName() string {
	return w.s.Spec.Template.Spec.ServiceAccountName
}

// RolloutComplete replicates the public contract of `kubectl rollout status`
// for StatefulSet. k8s.io/kubectl (pkg/polymorphichelpers, the package that
// owns StatefulSetStatusViewer) is NOT a dependency of this module and is not
// present in go.sum/the module cache, so this method could not be checked
// against the authoritative vendored source the way deploymentWorkload's
// RolloutComplete was. It is implemented instead from the well-documented,
// stable StatefulSetStatus field contract (ObservedGeneration,
// ReadyReplicas, UpdatedReplicas, CurrentReplicas, CurrentRevision,
// UpdateRevision) and from the publicly known behavior of `kubectl rollout
// status` on a StatefulSet. Flagging this explicitly: unverified against
// vendored source.
//
// Divergence from kubectl deliberately kept, per an explicit product
// decision for this project: kubectl refuses to report status at all for
// OnDeleteStatefulSetStrategyType (it returns an error telling the user
// rollout status is only available for RollingUpdate). This method does not
// refuse: once the generation and readiness checks pass, it reports the
// rollout complete under OnDelete too, because a dedicated diagnoser for
// "pending OnDelete update" is planned for `watch` (Phase B of StatefulSet
// support) and RolloutComplete must not block `check`/future `watch` forever
// on a strategy type it is allowed to not track precisely.
func (w *statefulSetWorkload) RolloutComplete() bool {
	s := w.s
	if s.Generation > s.Status.ObservedGeneration {
		return false
	}
	desired := w.Replicas()
	if s.Status.ReadyReplicas < desired {
		return false
	}

	strategy := s.Spec.UpdateStrategy
	strategyType := strategy.Type
	if strategyType == "" {
		strategyType = appsv1.RollingUpdateStatefulSetStrategyType
	}
	if strategyType == appsv1.OnDeleteStatefulSetStrategyType {
		return true
	}

	if strategy.RollingUpdate != nil && strategy.RollingUpdate.Partition != nil {
		threshold := desired - *strategy.RollingUpdate.Partition
		return s.Status.UpdatedReplicas >= threshold
	}

	return s.Status.UpdateRevision == s.Status.CurrentRevision
}

// ProgressDeadlineExceeded implements Workload. StatefulSet has no
// progressDeadlineSeconds field and no controller-computed "Progressing"
// condition: there is nothing for this method to read, so it always reports
// ok=false ("not applicable to this controller type"), matching the
// documented contract on the Workload interface itself.
func (w *statefulSetWorkload) ProgressDeadlineExceeded() (message string, ok bool) {
	return "", false
}

// Paused implements Workload. StatefulSet has no spec.paused field at all:
// this method exists on the interface only because Deployment has pause
// semantics that watch must account for. false here is a hard fact about the
// StatefulSet API (there is no pause mechanism to report on), not a
// placeholder default standing in for "unknown".
func (w *statefulSetWorkload) Paused() bool {
	return false
}
