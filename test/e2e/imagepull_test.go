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

// TestWatchE2E_InvalidImageReference verifies
// imagepull-invalid-reference: the malformed image name is rejected by
// the kubelet with InvalidImageName before any registry is contacted.
func TestWatchE2E_InvalidImageReference(t *testing.T) {
	client := newE2EClient(t)
	ns := newE2ENamespace(t, client)

	podSpec := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:  "app",
			Image: "BUSYBOX:Bad_Tag!!",
		}},
	}
	d := deployWorkload(t, client, ns, "pull-invalid-reference", 1, podSpec, nil)

	watchAndExpectCause(t, client, ns, d, diagnose.CauseImagePullInvalidReference, 2*time.Minute)
}

// TestWatchE2E_NonexistentImageTag verifies imagepull-tag-not-found: a
// tag that does not exist in a real, reachable repository (Docker Hub),
// so the kind node's container runtime actually returns "manifest
// unknown", not a simulated message.
func TestWatchE2E_NonexistentImageTag(t *testing.T) {
	client := newE2EClient(t)
	ns := newE2ENamespace(t, client)

	podSpec := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:  "app",
			Image: "busybox:this-tag-does-not-exist-v99",
		}},
	}
	d := deployWorkload(t, client, ns, "pull-nonexistent-tag", 1, podSpec, nil)

	watchAndExpectCause(t, client, ns, d, diagnose.CauseImagePullTagNotFound, 2*time.Minute)
}

// TestWatchE2E_UnreachableRegistry verifies
// imagepull-registry-unreachable: a registry hostname that does not
// resolve through DNS, producing a fast, deterministic failure.
func TestWatchE2E_UnreachableRegistry(t *testing.T) {
	client := newE2EClient(t)
	ns := newE2ENamespace(t, client)

	podSpec := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:  "app",
			Image: "registry.invalid.safe-rollout-e2e.example/app:v1",
		}},
	}
	d := deployWorkload(t, client, ns, "pull-registry-irraggiungibile", 1, podSpec, nil)

	watchAndExpectCause(t, client, ns, d, diagnose.CauseImagePullRegistryUnreachable, 2*time.Minute)
}

// TestWatchE2E_MissingRegistryCredentials verifies
// imagepull-credentials-missing: a private/nonexistent repository on
// Docker Hub without imagePullSecrets produces the classic "pull access
// denied ... repository does not exist or may require 'docker login'"
// message, which the pattern module classifies as missing credentials
// (not a nonexistent tag; see the comment in internal/diagnose/pattern).
func TestWatchE2E_MissingRegistryCredentials(t *testing.T) {
	client := newE2EClient(t)
	ns := newE2ENamespace(t, client)

	podSpec := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:  "app",
			Image: "docker.io/kubectlsaferolloute2e/nonexistent-private-repo:v1",
		}},
	}
	d := deployWorkload(t, client, ns, "pull-credenziali-mancanti", 1, podSpec, nil)

	watchAndExpectCause(t, client, ns, d, diagnose.CauseImagePullUnauthorized, 2*time.Minute)
}
