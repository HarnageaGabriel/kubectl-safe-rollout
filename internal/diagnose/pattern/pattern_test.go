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

package pattern_test

import (
	"testing"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/diagnose/pattern"
)

func TestImagePullFailure_TagNotFound(t *testing.T) {
	cause, ok := pattern.ImagePullFailure(`Failed to pull image "registry.example.com/app:v9.9.9": rpc error: code = NotFound desc = failed to pull and unpack image: failed to resolve reference: manifest unknown`)
	if !ok || cause != "tag-not-found" {
		t.Fatalf("cause=%q ok=%v, atteso tag-not-found", cause, ok)
	}
}

// TestImagePullFailure_TagNotFound_ContainerdReale usa il messaggio
// esatto osservato su kind v0.32 / containerd 2.2 (test/e2e,
// TestWatchE2E_ImageTagInesistente): il formato "manifest unknown" del
// test sopra copre containerd 1.x/CRI-O piu' vecchi, questo copre le
// versioni attuali. Entrambi restano perche' entrambi si osservano in
// pratica a seconda del cluster.
func TestImagePullFailure_TagNotFound_ContainerdReale(t *testing.T) {
	cause, ok := pattern.ImagePullFailure(`Failed to pull image "busybox:questo-tag-non-esiste-v99": rpc error: code = NotFound desc = failed to pull and unpack image "docker.io/library/busybox:questo-tag-non-esiste-v99": failed to resolve reference "docker.io/library/busybox:questo-tag-non-esiste-v99": docker.io/library/busybox:questo-tag-non-esiste-v99: not found`)
	if !ok || cause != "tag-not-found" {
		t.Fatalf("cause=%q ok=%v, atteso tag-not-found", cause, ok)
	}
}

// TestImagePullFailure_Unauthorized usa il messaggio reale osservato su
// kind per un repository privato/inesistente su Docker Hub senza
// imagePullSecrets (test/e2e, TestWatchE2E_CredenzialiRegistryMancanti).
// Contiene anche "failed to resolve reference", lo stesso prefisso
// generico del messaggio di tag-not-found: e' il caso che ha reso
// necessario escludere quel prefisso dal pattern di tag-not-found (vedi
// il commento in pattern.go).
func TestImagePullFailure_Unauthorized(t *testing.T) {
	cause, ok := pattern.ImagePullFailure(`Failed to pull image "docker.io/kubectlsaferolloute2e/repo-privata-inesistente:v1": failed to pull and unpack image "docker.io/kubectlsaferolloute2e/repo-privata-inesistente:v1": failed to resolve reference "docker.io/kubectlsaferolloute2e/repo-privata-inesistente:v1": pull access denied, repository does not exist or may require authorization: server message: insufficient_scope: authorization failed`)
	if !ok || cause != "unauthorized" {
		t.Fatalf("cause=%q ok=%v, atteso unauthorized", cause, ok)
	}
}

// TestImagePullFailure_RegistryUnreachable usa il messaggio reale
// osservato su kind per un hostname di registry che non risolve via DNS
// (test/e2e, TestWatchE2E_RegistryNonRaggiungibile). Stessa nota sul
// prefisso "failed to resolve reference" del test precedente.
func TestImagePullFailure_RegistryUnreachable(t *testing.T) {
	cause, ok := pattern.ImagePullFailure(`Failed to pull image "registry.invalid.safe-rollout-e2e.example/app:v1": failed to pull and unpack image "registry.invalid.safe-rollout-e2e.example/app:v1": failed to resolve reference "registry.invalid.safe-rollout-e2e.example/app:v1": failed to do request: Head "https://registry.invalid.safe-rollout-e2e.example/v2/app/manifests/v1": dial tcp: lookup registry.invalid.safe-rollout-e2e.example on 192.168.65.254:53: no such host`)
	if !ok || cause != "registry-unreachable" {
		t.Fatalf("cause=%q ok=%v, atteso registry-unreachable", cause, ok)
	}
}

func TestImagePullFailure_MessaggioNonRiconosciuto(t *testing.T) {
	_, ok := pattern.ImagePullFailure("qualcosa che non abbiamo mai visto prima")
	if ok {
		t.Fatal("atteso ok=false per un messaggio senza pattern noto")
	}
}

func TestLivenessKilling_Combacia(t *testing.T) {
	if !pattern.LivenessKilling("Killing", "Container app failed liveness probe, will be restarted", "app") {
		t.Fatal("atteso match per evento Killing con messaggio di liveness probe")
	}
}

func TestLivenessKilling_ReasonDiverso(t *testing.T) {
	if pattern.LivenessKilling("Unhealthy", "Liveness probe failed: HTTP probe failed with statuscode: 500", "app") {
		t.Fatal("Reason=Unhealthy non deve combaciare: e' il fallimento della probe, non l'uccisione del container")
	}
}

func TestLivenessKilling_ContainerDiverso(t *testing.T) {
	if pattern.LivenessKilling("Killing", "Container sidecar failed liveness probe, will be restarted", "app") {
		t.Fatal("il messaggio menziona un container diverso, non deve combaciare per 'app'")
	}
}

func TestFailedScheduling_RisorseInsufficienti(t *testing.T) {
	cause, ok := pattern.FailedScheduling("0/5 nodes are available: 5 Insufficient cpu.")
	if !ok || cause != "insufficient-resources" {
		t.Fatalf("cause=%q ok=%v, atteso insufficient-resources", cause, ok)
	}
}

func TestFailedScheduling_VincoliScheduling(t *testing.T) {
	cause, ok := pattern.FailedScheduling("0/5 nodes are available: 5 node(s) didn't match Pod's node affinity/selector.")
	if !ok || cause != "scheduling-constraints" {
		t.Fatalf("cause=%q ok=%v, atteso scheduling-constraints", cause, ok)
	}
}

func TestFailedScheduling_PVCNonBound(t *testing.T) {
	cause, ok := pattern.FailedScheduling("0/5 nodes are available: pod has unbound immediate PersistentVolumeClaims.")
	if !ok || cause != "unbound-pvc" {
		t.Fatalf("cause=%q ok=%v, atteso unbound-pvc", cause, ok)
	}
}

func TestFailedScheduling_MessaggioNonRiconosciuto(t *testing.T) {
	_, ok := pattern.FailedScheduling("qualcosa che non abbiamo mai visto prima")
	if ok {
		t.Fatal("atteso ok=false per un messaggio senza pattern noto")
	}
}

func TestQuotaExceeded_Combacia(t *testing.T) {
	if !pattern.QuotaExceeded(`pods "app-abc123" is forbidden: exceeded quota: compute-quota, requested: limits.cpu=1, used: limits.cpu=4, limited: limits.cpu=4`) {
		t.Fatal("atteso match per messaggio di quota superata")
	}
}

func TestQuotaExceeded_MessaggioDiverso(t *testing.T) {
	if pattern.QuotaExceeded(`admission webhook "policy.example.com" denied the request`) {
		t.Fatal("un rifiuto da webhook non e' una quota superata")
	}
}
