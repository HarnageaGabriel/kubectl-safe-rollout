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

package diagnose

// CauseID identifica in modo stabile una causa classificata di
// fallimento o stallo di un rollout. E' il valore che ogni Diagnoser
// scrive in model.Finding.CheckID (non "crashloop", il nome della
// categoria: la causa specifica, es. "crashloop-oomkilled"), cosi' chi
// consuma l'output json puo' fare gating in CI su una singola causa
// invece che sull'intera categoria.
//
// Ogni categoria (crashloop, imagepull, pending) ha una propria
// variante "-undetermined": e' la causa che un Diagnoser riporta quando
// l'evidenza disponibile conferma che il rollout e' bloccato ma non
// basta a distinguere tra le cause note della categoria. Non esiste un
// valore CauseID generico per "non determinato senza contesto": il
// vincolo di determinismo del progetto richiede che anche un
// non-determinato dichiari almeno la categoria in cui e' stato
// osservato, mai un'etichetta vuota.
type CauseID string

const (
	// CauseCrashLoopOOMKilled il container viene ucciso dal kernel per
	// superamento del limite di memoria. Segnale strutturato
	// (ContainerStateTerminated.Reason == "OOMKilled"), nessun parsing
	// di testo libero necessario.
	CauseCrashLoopOOMKilled CauseID = "crashloop-oomkilled"
	// CauseCrashLoopLivenessProbe il kubelet termina il container
	// perche' la liveness probe fallisce ripetutamente, non perche'
	// l'app crasha da sola. Segnale: evento Reason=="Killing" con
	// messaggio che menziona la liveness probe.
	CauseCrashLoopLivenessProbe CauseID = "crashloop-liveness-probe"
	// CauseCrashLoopAppError il container termina con un exit code
	// applicativo, ne' OOMKilled ne' ucciso dalla probe.
	CauseCrashLoopAppError CauseID = "crashloop-app-error"
	// CauseCrashLoopUndetermined il container e' in CrashLoopBackOff
	// ma l'evidenza disponibile (LastTerminationState assente o
	// Reason vuoto, nessun evento di Killing per liveness) non permette
	// di scegliere tra le tre cause sopra.
	CauseCrashLoopUndetermined CauseID = "crashloop-undetermined"

	// CauseImagePullTagNotFound il tag o il digest richiesto non
	// esiste sul registry.
	CauseImagePullTagNotFound CauseID = "imagepull-tag-not-found"
	// CauseImagePullRegistryUnreachable il registry non risponde (DNS,
	// rete, timeout), non e' un problema di autorizzazione o di tag.
	CauseImagePullRegistryUnreachable CauseID = "imagepull-registry-unreachable"
	// CauseImagePullUnauthorized il pull viene rifiutato per
	// autenticazione/autorizzazione mancante o insufficiente
	// (imagePullSecrets assente o scaduto).
	CauseImagePullUnauthorized CauseID = "imagepull-credentials-missing"
	// CauseImagePullUndetermined il container e' in
	// ImagePullBackOff/ErrImagePull ma il messaggio dell'evento Failed
	// non combacia con nessun pattern noto (runtime o formato di
	// messaggio non previsto).
	CauseImagePullUndetermined CauseID = "imagepull-undetermined"

	// CausePendingInsufficientResources nessun nodo ha CPU/memoria/
	// storage effimero sufficiente per lo scheduling.
	CausePendingInsufficientResources CauseID = "pending-insufficient-resources"
	// CausePendingSchedulingConstraints nodeSelector, affinity o
	// taint/toleration impediscono lo scheduling su qualunque nodo
	// disponibile.
	CausePendingSchedulingConstraints CauseID = "pending-scheduling-constraints"
	// CausePendingUnboundPVC il pod referenzia una
	// PersistentVolumeClaim non ancora Bound (o inesistente). Segnale
	// strutturato: PersistentVolumeClaim.Status.Phase, non un pattern
	// di testo.
	CausePendingUnboundPVC CauseID = "pending-unbound-pvc"
	// CausePendingUndetermined il pod e' Pending ma non c'e' un evento
	// FailedScheduling riconoscibile ne' una PVC non bound referenziata.
	CausePendingUndetermined CauseID = "pending-undetermined"

	// CauseQuotaExceeded il ReplicaSet non riesce a creare pod perche'
	// l'admission plugin di ResourceQuota rifiuta la richiesta. Non
	// osservabile sui Pod (non arrivano a esistere): osservabile solo
	// come evento Reason=="FailedCreate" sul ReplicaSet.
	CauseQuotaExceeded CauseID = "quota-exceeded"
	// CauseQuotaUndetermined il ReplicaSet ha un evento FailedCreate
	// ma il messaggio non menziona una quota superata (potrebbe essere
	// un webhook di ammissione o un altro rifiuto): il fallimento nella
	// creazione dei pod e' comunque un segnale reale da riportare, la
	// causa specifica no.
	CauseQuotaUndetermined CauseID = "quota-undetermined"

	// CauseProgressDeadlineExceeded il controller del Deployment ha
	// gia' concluso, per conto proprio, che il rollout non progredisce
	// entro spec.progressDeadlineSeconds. Non c'e' ambiguita' da
	// risolvere qui: la condizione stessa e' la causa, letta da
	// Status.Conditions, non derivata da un pattern di testo.
	CauseProgressDeadlineExceeded CauseID = "progress-deadline-exceeded"
)
