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

// TestWatchE2E_QuotaEsaurita verifica quota-exceeded: una ResourceQuota
// che permette un solo pod nel namespace, un Deployment con 2 repliche
// che la supera. Il secondo pod non arriva mai a esistere: l'admission
// plugin rifiuta la creazione e il ReplicaSet riceve FailedCreate.
func TestWatchE2E_QuotaEsaurita(t *testing.T) {
	client := newE2EClient(t)
	ns := newE2ENamespace(t, client)

	quota := &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{Name: "tight-quota", Namespace: ns},
		Spec: corev1.ResourceQuotaSpec{
			Hard: corev1.ResourceList{
				corev1.ResourcePods: resource.MustParse("1"),
			},
		},
	}
	if _, err := client.CoreV1().ResourceQuotas(ns).Create(t.Context(), quota, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creazione ResourceQuota: %v", err)
	}

	podSpec := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:    "app",
			Image:   "busybox:1.36",
			Command: []string{"sh", "-c", "sleep 3600"},
		}},
	}
	d := deployWorkload(t, client, ns, "quota-esaurita", 2, podSpec, nil)

	watchAndExpectCause(t, client, ns, d, diagnose.CauseQuotaExceeded, 2*time.Minute)
}
