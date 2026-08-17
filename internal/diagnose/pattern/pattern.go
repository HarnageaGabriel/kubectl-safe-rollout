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

// Package pattern isola tutto il parsing di testo libero (Event.Message
// di kubelet, scheduler e container runtime) usato da internal/diagnose.
// E' un punto solo, deliberatamente: questi messaggi non sono un'API
// stabile di Kubernetes, cambiano tra versioni e tra runtime (containerd
// vs CRI-O producono testi diversi per lo stesso errore di pull), e
// spargere string matching nella logica di classificazione renderebbe
// impossibile capire, aggiornare o testare in un colpo solo cosa questo
// progetto riconosce.
//
// Ogni funzione qui distingue segnali strutturati da segnali testuali:
// dove Kubernetes espone un campo stabile (Reason di un Event o di un
// ContainerState, Phase di una PersistentVolumeClaim), i Diagnoser lo
// usano direttamente e non passano da questo package. Qui vive solo cio'
// che non ha alternativa strutturata. Ogni funzione restituisce ok=false
// quando nessun pattern noto combacia: il chiamante deve trattarlo come
// causa non determinata, mai scegliere a indovinare.
package pattern

import "strings"

// ImagePullFailure classifica il messaggio di un evento Reason=="Failed"
// generato durante il pull di un'immagine (tipicamente affiancato da un
// container in stato Waiting con Reason "ImagePullBackOff" o
// "ErrImagePull", che il chiamante ha gia' verificato separatamente
// tramite il campo strutturato).
func ImagePullFailure(message string) (cause string, ok bool) {
	m := strings.ToLower(message)
	switch {
	// "repository does not exist" e' deliberatamente escluso da questo
	// caso: e' anche il testo del messaggio classico di Docker per un
	// pull rifiutato per autorizzazione ("pull access denied for X,
	// repository does not exist or may require 'docker login'"), non
	// solo per un'immagine davvero assente. "failed to resolve
	// reference" e' escluso allo stesso modo: e' un prefisso generico
	// che containerd usa per QUALUNQUE fallimento di risoluzione
	// (verificato su kind v0.32/containerd 2.2: compare identico nei
	// messaggi di tag inesistente, credenziali mancanti e registry
	// irraggiungibile), non specifico di "non trovato". Il suffisso
	// ": not found" invece lo e' davvero: nel messaggio reale di
	// containerd 2.x compare solo quando il tag/digest non esiste
	// ("...: docker.io/library/busybox:tag: not found"). "manifest
	// unknown"/"manifest for ... not found" coprono containerd 1.x e
	// CRI-O piu' vecchi.
	case containsAny(m, ": not found", "manifest unknown", "manifest for", "name unknown"):
		return "tag-not-found", true
	case containsAny(m, "unauthorized", "pull access denied", "insufficient_scope", "authentication required", "does not exist or may require", "401", "403"):
		return "unauthorized", true
	case containsAny(m, "no such host", "i/o timeout", "connection refused", "context deadline exceeded", "tls handshake timeout", "network is unreachable", "no route to host"):
		return "registry-unreachable", true
	default:
		return "", false
	}
}

// LivenessKilling riconosce l'evento che il kubelet genera quando
// termina un container per fallimento ripetuto della liveness probe.
// Reason=="Killing" e' un valore stabile (costante interna del
// kubelet, non pensata per cambiare: kubectl describe e molti tool di
// osservabilita' vi si affidano gia'). Il messaggio associato e' testo
// libero ("Container %s failed liveness probe, will be restarted"): la
// combinazione dei due e' il segnale piu' affidabile disponibile senza
// importare k8s.io/kubernetes, che non e' una dipendenza pensata per
// consumo esterno.
func LivenessKilling(reason, message, container string) bool {
	if reason != "Killing" {
		return false
	}
	m := strings.ToLower(message)
	if !strings.Contains(m, "liveness probe") {
		return false
	}
	return container == "" || strings.Contains(message, container)
}

// FailedScheduling classifica il messaggio di un evento
// Reason=="FailedScheduling" generato dallo scheduler per un pod
// Pending.
func FailedScheduling(message string) (cause string, ok bool) {
	m := strings.ToLower(message)
	switch {
	case containsAny(m, "unbound immediate persistentvolumeclaims", "unbound persistentvolumeclaims", "persistentvolumeclaim not found", "waiting for first consumer"):
		return "unbound-pvc", true
	case containsAny(m, "insufficient cpu", "insufficient memory", "insufficient ephemeral-storage", "insufficient pods"):
		return "insufficient-resources", true
	case containsAny(m, "didn't match pod's node affinity", "didn't match pod's node selector", "node(s) had taint", "didn't tolerate", "node(s) didn't match node selector", "node(s) had untolerated taint"):
		return "scheduling-constraints", true
	default:
		return "", false
	}
}

// QuotaExceeded riconosce il messaggio di un evento Reason=="FailedCreate"
// su un ReplicaSet bloccato dall'admission plugin di ResourceQuota. Il
// prefisso "exceeded quota:" e' generato da k8s.io/apiserver/plugin/pkg/
// admission/resourcequota ed e' stabile da molte major version, al
// contrario dei messaggi di scheduler/kubelet sopra: e' comunque isolato
// qui insieme agli altri, non nella logica del Diagnoser, per lo stesso
// motivo di manutenibilita'.
func QuotaExceeded(message string) bool {
	return strings.Contains(strings.ToLower(message), "exceeded quota")
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
