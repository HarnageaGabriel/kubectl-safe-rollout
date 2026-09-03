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

package e2e_test

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/diagnose"
)

// TestWatchE2E_InsufficientResources verifies
// pending-insufficient-resources: a CPU request much larger than what is
// available on the single kind node, so the scheduler immediately
// rejects it with FailedScheduling.
func TestWatchE2E_InsufficientResources(t *testing.T) {
	client := newE2EClient(t)
	ns := newE2ENamespace(t, client)

	podSpec := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:    "app",
			Image:   "busybox:1.36",
			Command: []string{"sh", "-c", "sleep 3600"},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("1000"),
				},
			},
		}},
	}
	d := deployWorkload(t, client, ns, "pending-risorse", 1, podSpec, nil)

	watchAndExpectCause(t, client, ns, d, diagnose.CausePendingInsufficientResources, 2*time.Minute)
}

// TestWatchE2E_SchedulingConstraints verifies
// pending-scheduling-constraints: a nodeSelector that does not match any
// node in the single-node kind cluster.
func TestWatchE2E_SchedulingConstraints(t *testing.T) {
	client := newE2EClient(t)
	ns := newE2ENamespace(t, client)

	podSpec := corev1.PodSpec{
		NodeSelector: map[string]string{"this-label-does-not-exist-on-any-node": "true"},
		Containers: []corev1.Container{{
			Name:    "app",
			Image:   "busybox:1.36",
			Command: []string{"sh", "-c", "sleep 3600"},
		}},
	}
	d := deployWorkload(t, client, ns, "pending-scheduling", 1, podSpec, nil)

	watchAndExpectCause(t, client, ns, d, diagnose.CausePendingSchedulingConstraints, 2*time.Minute)
}

// TestWatchE2E_UnboundPVC verifies pending-unbound-pvc: a
// PersistentVolumeClaim with a nonexistent storageClassName never finds
// a provisioner and remains Pending indefinitely.
func TestWatchE2E_UnboundPVC(t *testing.T) {
	client := newE2EClient(t)
	ns := newE2ENamespace(t, client)

	storageClass := "this-storageclass-does-not-exist"
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "data-pvc", Namespace: ns},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			StorageClassName: &storageClass,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Mi")},
			},
		},
	}
	if _, err := client.CoreV1().PersistentVolumeClaims(ns).Create(t.Context(), pvc, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating PersistentVolumeClaim: %v", err)
	}

	podSpec := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:    "app",
			Image:   "busybox:1.36",
			Command: []string{"sh", "-c", "sleep 3600"},
			VolumeMounts: []corev1.VolumeMount{{
				Name:      "data",
				MountPath: "/data",
			}},
		}},
		Volumes: []corev1.Volume{{
			Name: "data",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data-pvc"},
			},
		}},
	}
	d := deployWorkload(t, client, ns, "pending-pvc", 1, podSpec, nil)

	watchAndExpectCause(t, client, ns, d, diagnose.CausePendingUnboundPVC, 2*time.Minute)
}
