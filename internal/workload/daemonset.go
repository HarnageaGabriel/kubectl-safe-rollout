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
	"k8s.io/apimachinery/pkg/util/intstr"
)

type daemonSetWorkload struct {
	d *appsv1.DaemonSet
}

// FromDaemonSet builds a Workload from a live DaemonSet. Verified against
// k8s.io/api@v0.37.0/apps/v1/types.go: DaemonSetSpec carries no Replicas, no
// Paused, no ProgressDeadlineSeconds and no VolumeClaimTemplates field at
// all (unlike Deployment/StatefulSet), and DaemonSetStatus carries no
// CurrentRevision/UpdateRevision pair (unlike StatefulSet) and no
// "Progressing" condition type (DaemonSetConditionType declares zero
// constants). Every method below that stands in for one of those concepts
// returns the "not applicable to this controller type" value already
// established by statefulSetWorkload's equivalents, not a guess.
func FromDaemonSet(d *appsv1.DaemonSet) Workload {
	return &daemonSetWorkload{d: d}
}

func (w *daemonSetWorkload) Kind() string      { return "DaemonSet" }
func (w *daemonSetWorkload) Name() string      { return w.d.Name }
func (w *daemonSetWorkload) Namespace() string { return w.d.Namespace }
func (w *daemonSetWorkload) UID() types.UID    { return w.d.UID }

// Replicas implements Workload. DaemonSet has no spec.replicas field: the
// desired count is entirely controller-computed, the number of nodes
// eligible to run the pod (status.desiredNumberScheduled), not a value a
// user sets directly.
func (w *daemonSetWorkload) Replicas() int32 {
	return w.d.Status.DesiredNumberScheduled
}

func (w *daemonSetWorkload) PodLabels() map[string]string {
	return w.d.Spec.Template.Labels
}

func (w *daemonSetWorkload) PodSelector() (labels.Selector, error) {
	return metav1.LabelSelectorAsSelector(w.d.Spec.Selector)
}

// UpdateStrategy normalizes DaemonSet's update strategy into the shared
// UpdateStrategy shape. Unlike StatefulSet (where MaxUnavailable's default
// is not affirmable across every supported server version), DaemonSet's
// defaulting is unconditional and gate-free as of the k8s.io/api version
// this module depends on: SetDefaults_DaemonSet
// (k8s.io/kubernetes@v1.36.1/pkg/apis/apps/v1/defaults.go, lines 75-97)
// always sets RollingUpdate.MaxUnavailable to 1 and RollingUpdate.MaxSurge
// to 0 whenever they are left nil, so this method applies the same
// defaults rather than leaving them nil. Partition has no DaemonSet
// equivalent at all (the field does not exist on
// RollingUpdateDaemonSet): always nil, the same convention already used by
// Deployment's UpdateStrategy for a StatefulSet-only concept.
func (w *daemonSetWorkload) UpdateStrategy() UpdateStrategy {
	strategyType := w.d.Spec.UpdateStrategy.Type
	if strategyType == "" {
		strategyType = RollingUpdate
	}
	if strategyType == appsv1.OnDeleteDaemonSetStrategyType {
		return UpdateStrategy{Type: OnDelete}
	}

	ru := w.d.Spec.UpdateStrategy.RollingUpdate
	maxUnavailable := intstr.FromInt32(1)
	maxSurge := intstr.FromInt32(0)
	if ru != nil {
		if ru.MaxUnavailable != nil {
			maxUnavailable = *ru.MaxUnavailable
		}
		if ru.MaxSurge != nil {
			maxSurge = *ru.MaxSurge
		}
	}
	return UpdateStrategy{
		Type:           RollingUpdate,
		MaxUnavailable: &maxUnavailable,
		MaxSurge:       &maxSurge,
	}
}

// PodRequests implements Workload.
func (w *daemonSetWorkload) PodRequests() corev1.ResourceList {
	total := corev1.ResourceList{}
	for _, c := range w.d.Spec.Template.Spec.Containers {
		for name, qty := range c.Resources.Requests {
			sum := total[name]
			sum.Add(qty)
			total[name] = sum
		}
	}
	return total
}

// PodContainers implements Workload.
func (w *daemonSetWorkload) PodContainers() []corev1.Container {
	return w.d.Spec.Template.Spec.Containers
}

// InitContainers implements Workload.
func (w *daemonSetWorkload) InitContainers() []corev1.Container {
	return w.d.Spec.Template.Spec.InitContainers
}

// Volumes implements Workload.
func (w *daemonSetWorkload) Volumes() []corev1.Volume {
	return w.d.Spec.Template.Spec.Volumes
}

// ImagePullSecretNames implements Workload.
func (w *daemonSetWorkload) ImagePullSecretNames() []string {
	refs := w.d.Spec.Template.Spec.ImagePullSecrets
	names := make([]string, 0, len(refs))
	for _, ref := range refs {
		names = append(names, ref.Name)
	}
	return names
}

// ServiceAccountName implements Workload.
func (w *daemonSetWorkload) ServiceAccountName() string {
	return w.d.Spec.Template.Spec.ServiceAccountName
}

// PriorityClassName implements Workload.
func (w *daemonSetWorkload) PriorityClassName() string {
	return w.d.Spec.Template.Spec.PriorityClassName
}

// TopologySpreadConstraints implements Workload.
func (w *daemonSetWorkload) TopologySpreadConstraints() []corev1.TopologySpreadConstraint {
	return w.d.Spec.Template.Spec.TopologySpreadConstraints
}

// Affinity implements Workload.
func (w *daemonSetWorkload) Affinity() *corev1.Affinity {
	return w.d.Spec.Template.Spec.Affinity
}

// NodeSelector implements Workload.
func (w *daemonSetWorkload) NodeSelector() map[string]string {
	return w.d.Spec.Template.Spec.NodeSelector
}

// Tolerations implements Workload.
func (w *daemonSetWorkload) Tolerations() []corev1.Toleration {
	return w.d.Spec.Template.Spec.Tolerations
}

// SchedulerName implements Workload.
func (w *daemonSetWorkload) SchedulerName() string {
	return w.d.Spec.Template.Spec.SchedulerName
}

// RolloutComplete has been checked line by line against the real
// DaemonSetStatusViewer.Status implementation in
// k8s.io/kubectl@v0.36.3/pkg/polymorphichelpers/rollout_status.go, lines
// 95-117: it requires the strategy to be RollingUpdate (kubectl itself
// refuses to report status for OnDelete, returning an error), then that
// Generation has been observed, that UpdatedNumberScheduled has reached
// DesiredNumberScheduled, and that NumberAvailable has reached
// DesiredNumberScheduled. This method replicates all four conditions
// exactly, with the same OnDelete divergence already adopted for
// StatefulSet (see statefulSetWorkload.RolloutComplete): kubectl refuses to
// track OnDelete at all, this method evaluates the same generation/count
// checks and reports the real state instead of refusing, because a
// dedicated diagnoser for "pending OnDelete update" is planned for `watch`
// and RolloutComplete must not block forever on a strategy type it is
// allowed to not track precisely.
func (w *daemonSetWorkload) RolloutComplete() bool {
	d := w.d
	if d.Generation > d.Status.ObservedGeneration {
		return false
	}
	desired := d.Status.DesiredNumberScheduled
	if d.Status.UpdatedNumberScheduled < desired {
		return false
	}
	if d.Status.NumberAvailable < desired {
		return false
	}
	return true
}

// ProgressDeadlineExceeded implements Workload. DaemonSet has no
// progressDeadlineSeconds field and DaemonSetConditionType declares zero
// condition constants (k8s.io/api@v0.37.0/apps/v1/types.go, line 787-790):
// there is no "Progressing" condition for this controller to compute, so
// this always reports ok=false, the same convention already used by
// statefulSetWorkload.
func (w *daemonSetWorkload) ProgressDeadlineExceeded() (message string, ok bool) {
	return "", false
}

// Paused implements Workload. DaemonSet has no spec.paused field at all: a
// hard fact about the DaemonSet API, not a placeholder for "unknown".
func (w *daemonSetWorkload) Paused() bool {
	return false
}

// PendingRevisionUpdate implements Workload. DaemonSetStatus carries no
// CurrentRevision/UpdateRevision pair (unlike StatefulSet): the
// ControllerRevision hash for a DaemonSet is only ever visible as the pod
// label controller-revision-hash, never as a Status field, so there is
// nothing for this method to read. Always ok=false.
func (w *daemonSetWorkload) PendingRevisionUpdate() (updateRevision, currentRevision string, ok bool) {
	return "", "", false
}

// VolumeClaimTemplates implements Workload. DaemonSetSpec has no
// volumeClaimTemplates field at all: always nil, a hard fact about the
// DaemonSet API.
func (w *daemonSetWorkload) VolumeClaimTemplates() []corev1.PersistentVolumeClaim {
	return nil
}

// DesiredCount implements Workload: DesiredNumberScheduled is the
// controller's own count of eligible nodes, already what Replicas()
// returns for this type; observed mirrors the same generation-catch-up
// check used by RolloutComplete and by the other two adapters.
func (w *daemonSetWorkload) DesiredCount() (count int32, observed bool) {
	return w.d.Status.DesiredNumberScheduled, w.d.Generation <= w.d.Status.ObservedGeneration
}
