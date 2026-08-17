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

// TestWatchE2E_ImageTagInesistente verifica imagepull-tag-not-found: un
// tag che non esiste su un repository reale e raggiungibile (Docker
// Hub), cosi' il container runtime del nodo kind risponde "manifest
// unknown" davvero, non un messaggio simulato.
func TestWatchE2E_ImageTagInesistente(t *testing.T) {
	client := newE2EClient(t)
	ns := newE2ENamespace(t, client)

	podSpec := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:  "app",
			Image: "busybox:questo-tag-non-esiste-v99",
		}},
	}
	d := deployWorkload(t, client, ns, "pull-tag-inesistente", 1, podSpec, nil)

	watchAndExpectCause(t, client, ns, d, diagnose.CauseImagePullTagNotFound, 2*time.Minute)
}

// TestWatchE2E_RegistryNonRaggiungibile verifica
// imagepull-registry-unreachable: un hostname di registry che non
// risolve via DNS, fallimento rapido e deterministico.
func TestWatchE2E_RegistryNonRaggiungibile(t *testing.T) {
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

// TestWatchE2E_CredenzialiRegistryMancanti verifica
// imagepull-credentials-missing: un repository privato/inesistente su
// Docker Hub senza imagePullSecrets produce il messaggio classico "pull
// access denied ... repository does not exist or may require 'docker
// login'", che il pattern module classifica come credenziali mancanti
// (non tag inesistente, vedi il commento in internal/diagnose/pattern).
func TestWatchE2E_CredenzialiRegistryMancanti(t *testing.T) {
	client := newE2EClient(t)
	ns := newE2ENamespace(t, client)

	podSpec := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:  "app",
			Image: "docker.io/kubectlsaferolloute2e/repo-privata-inesistente:v1",
		}},
	}
	d := deployWorkload(t, client, ns, "pull-credenziali-mancanti", 1, podSpec, nil)

	watchAndExpectCause(t, client, ns, d, diagnose.CauseImagePullUnauthorized, 2*time.Minute)
}
