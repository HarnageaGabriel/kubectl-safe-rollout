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

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/diagnose"
)

// TestWatchE2E_MissingConfigMapContainerConfiguration verifies
// configerror-missing-configmap: envFrom references a ConfigMap that
// does not exist, producing CreateContainerConfigError.
func TestWatchE2E_MissingConfigMapContainerConfiguration(t *testing.T) {
	client := newE2EClient(t)
	ns := newE2ENamespace(t, client)

	podSpec := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:    "app",
			Image:   "busybox:1.36",
			Command: []string{"sh", "-c", "sleep 3600"},
			EnvFrom: []corev1.EnvFromSource{{
				ConfigMapRef: &corev1.ConfigMapEnvSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: "does-not-exist"},
				},
			}},
		}},
	}
	d := deployWorkload(t, client, ns, "config-missing-configmap", 1, podSpec, nil)

	watchAndExpectCause(t, client, ns, d, diagnose.CauseConfigErrorMissingConfigMap, 2*time.Minute)
}
