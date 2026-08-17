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

// Package diagnose implementa la classificazione delle cause di
// fallimento o stallo di un rollout osservato da `kubectl safe-rollout
// watch`. Ogni causa vive nel proprio file e implementa l'interfaccia
// Diagnoser, con lo stesso design di internal/check: aggiungere una
// classificazione non tocca le esistenti, e ognuna si testa in
// isolamento con client-go/kubernetes/fake.
//
// Vincolo non negoziabile: la classificazione e' deterministica. Se
// l'evidenza raccolta non basta a distinguere tra le cause note di una
// categoria, il Diagnoser riporta la variante "-undetermined" di quella
// categoria (vedi cause.go) con model.Finding.Undetermined a true e
// Evidence che elenca cosa e' stato osservato. Mai una causa scelta a
// indovinare: su un cluster di produzione una diagnosi sbagliata costa
// piu' del silenzio.
package diagnose

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

// LogTailer recupera le ultime righe di log del container precedente,
// usate come evidenza supplementare per le cause di CrashLoopBackOff.
// Non influenza mai la classificazione, che si basa solo su segnali
// strutturati (ContainerStatus) e su Event: se il fetch fallisce o
// LogTailer e' nil, il Finding resta valido, solo con una riga di
// evidenza in meno. E' un'interfaccia, non un client concreto, per
// restare testabile senza un apiserver reale.
type LogTailer interface {
	PreviousLogTail(ctx context.Context, namespace, pod, container string, lines int64) (string, error)
}

// Target raggruppa lo stato di un rollout osservato in un dato istante,
// gia' letto dal chiamante (watch.go, una volta per tick). I Diagnoser
// non fanno le proprie List/Get sul cluster salvo eccezioni puntuali
// (es. lettura di una singola PersistentVolumeClaim referenziata da un
// pod specifico): questo ammortizza il costo delle List piu' pesanti
// (Event dell'intero namespace) su un'unica lettura per tick, condivisa
// da tutti i Diagnoser registrati.
type Target struct {
	Namespace string
	Workload  workload.Workload
	Client    kubernetes.Interface
	// Pods sono i pod correnti del workload, gia' filtrati per label
	// selector dal chiamante.
	Pods []corev1.Pod
	// ReplicaSets sono i ReplicaSet posseduti dal Deployment osservato,
	// necessari solo al Diagnoser Quota (gli eventi di quota esaurita
	// vivono sul ReplicaSet, non sul Pod: il pod non arriva a esistere).
	ReplicaSets []appsv1.ReplicaSet
	// EventsByUID indicizza gli Event del namespace per UID
	// dell'oggetto coinvolto (Pod o ReplicaSet). Vedi
	// GroupEventsByInvolvedObject.
	EventsByUID map[types.UID][]corev1.Event
	// LogTailer puo' essere nil: nessuna evidenza di log supplementare,
	// la classificazione non ne dipende.
	LogTailer LogTailer
}

// Result e' l'esito dell'esecuzione di un singolo Diagnoser. Mirror
// deliberato di check.Result: Skipped distingue "nessuna causa nota
// osservata" (Findings vuoto, Skipped false) da "non sono riuscito a
// valutare questa categoria" (Skipped true, SkipReason valorizzato,
// tipicamente per un errore di RBAC su una risorsa puntuale come una
// PersistentVolumeClaim).
type Result struct {
	DiagnoserID string
	Findings    []model.Finding
	Skipped     bool
	SkipReason  string
}

// Diagnoser e' l'interfaccia che ogni classificazione di causa
// implementa. Mirror di check.Check: ID e' l'identificativo stabile
// della categoria (non della singola causa, vedi CauseID), Diagnose non
// scrive mai sul cluster e non fallisce mai per una condizione attesa
// del cluster (quella si esprime con Result.Skipped, un errore e'
// riservato a bug interni).
type Diagnoser interface {
	ID() string
	Diagnose(ctx context.Context, target Target) (Result, error)
}

// SkipResult costruisce un Result che dichiara esplicitamente
// l'impossibilita' di valutare una categoria, invece di un Result vuoto
// indistinguibile da "nessuna causa nota osservata".
func SkipResult(id, reason string) Result {
	return Result{DiagnoserID: id, Skipped: true, SkipReason: reason}
}

// registeredDiagnosers elenca tutte le classificazioni eseguite da
// RunDiagnosis. E' l'unico punto da toccare per aggiungere una nuova
// causa classificata.
func registeredDiagnosers() []Diagnoser {
	return []Diagnoser{
		CrashLoop{},
		ImagePull{},
		Pending{},
		Quota{},
		ProgressDeadline{},
	}
}

// RunDiagnosis esegue tutti i Diagnoser registrati sullo stato
// osservato in target e restituisce il Result di ciascuno, nell'ordine
// di registrazione. Un errore da un Diagnoser interrompe l'intera
// diagnosi: e' riservato a bug interni (es. costruzione di un selector
// fallita per un bug nostro), non a condizioni attese del cluster.
func RunDiagnosis(ctx context.Context, target Target) ([]Result, error) {
	diagnosers := registeredDiagnosers()
	results := make([]Result, 0, len(diagnosers))
	for _, d := range diagnosers {
		res, err := d.Diagnose(ctx, target)
		if err != nil {
			return nil, fmt.Errorf("diagnosi %s: %w", d.ID(), err)
		}
		results = append(results, res)
	}
	return results, nil
}

// GroupEventsByInvolvedObject indicizza gli Event per UID dell'oggetto
// coinvolto (Pod o ReplicaSet), cosi' ogni Diagnoser accede agli eventi
// rilevanti con una lookup invece di scansionare l'intera lista degli
// Event del namespace a ogni pod.
func GroupEventsByInvolvedObject(events []corev1.Event) map[types.UID][]corev1.Event {
	grouped := make(map[types.UID][]corev1.Event)
	for _, e := range events {
		uid := e.InvolvedObject.UID
		if uid == "" {
			continue
		}
		grouped[uid] = append(grouped[uid], e)
	}
	return grouped
}

// AnyFindings riporta se almeno un Result porta almeno un Finding. E'
// quello che il loop di watch.go usa per decidere che il rollout ha una
// causa diagnosticabile e fermare l'osservazione.
func AnyFindings(results []Result) bool {
	for _, r := range results {
		if len(r.Findings) > 0 {
			return true
		}
	}
	return false
}

// AllFindings appiattisce i Findings di tutti i Result, nell'ordine dei
// Result. Usato dal chiamante (watch.go) per costruire l'Outcome finale
// senza che ogni chiamante ripeta lo stesso ciclo.
func AllFindings(results []Result) []model.Finding {
	var findings []model.Finding
	for _, r := range results {
		findings = append(findings, r.Findings...)
	}
	return findings
}
