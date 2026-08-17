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

// Package workload astrae le differenze tra i tipi di workload
// Kubernetes (Deployment, e in futuro StatefulSet) dietro un'unica
// interfaccia. Le verifiche in internal/check ragionano su Workload, non
// sui tipi concreti di k8s.io/api/apps/v1: questo evita che ogni nuova
// verifica debba gestire uno switch su Deployment/StatefulSet/DaemonSet.
package workload

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// UpdateStrategy normalizza le strategie di aggiornamento dei diversi
// controller (RollingUpdate/Recreate per Deployment, RollingUpdate/
// OnDelete per StatefulSet) in una forma comune. MaxUnavailable e
// MaxSurge sono nil quando il controller non li supporta (es. Recreate),
// non quando valgono zero: la distinzione conta per le verifiche.
type UpdateStrategy struct {
	Type           string
	MaxUnavailable *intstr.IntOrString
	MaxSurge       *intstr.IntOrString
}

// Recreate e RollingUpdate replicano le costanti di appsv1 per non
// forzare i chiamanti a importare k8s.io/api/apps/v1 solo per
// confrontare il tipo di strategia.
const (
	Recreate        = "Recreate"
	RollingUpdate   = "RollingUpdate"
	defaultMaxUnav  = "25%"
	defaultMaxSurge = "25%"
)

// Workload e' la vista che le verifiche pre-flight e la diagnosi di
// watch hanno di un Deployment/StatefulSet/... live.
type Workload interface {
	Kind() string
	Name() string
	Namespace() string
	// UID identifica l'oggetto live: serve a internal/diagnose per
	// filtrare i ReplicaSet posseduti dal workload (OwnerReferences),
	// non alle verifiche di internal/check, che non ne hanno bisogno.
	UID() types.UID
	// Replicas e' il numero di repliche desiderate, mai nil: un
	// Deployment con Spec.Replicas non impostato vale 1 per default di
	// Kubernetes, e questa funzione applica quel default cosi' i
	// chiamanti non devono ricordarselo.
	Replicas() int32
	// PodLabels sono le label del pod template, usate per il matching
	// contro il selector di un PodDisruptionBudget e per l'osservazione
	// dei pod in `watch`.
	PodLabels() map[string]string
	// PodSelector e' il selector immutabile del controller, non tutte le
	// label del template corrente. Serve a includere anche pod e ReplicaSet
	// di revisioni precedenti durante un rollout.
	PodSelector() (labels.Selector, error)
	UpdateStrategy() UpdateStrategy
	// PodRequests somma le request (non i limits) di tutti i container
	// regolari del pod template, per confrontarle con l'headroom di una
	// ResourceQuota durante il surge. Somma solo i container regolari:
	// gli init container hanno una semantica di request effettiva
	// diversa (il massimo tra loro e la somma dei regolari, non un
	// ulteriore addendo), un dettaglio che la sola verifica di quota-
	// headroom non ha bisogno di modellare per un margine ragionevole.
	PodRequests() corev1.ResourceList
	// RolloutComplete riporta se il rollout e' concluso con successo:
	// repliche aggiornate, disponibili, e la generazione corrente della
	// Spec osservata dal controller. Usato da `watch` per fermare
	// l'osservazione sul percorso positivo.
	RolloutComplete() bool
	// ProgressDeadlineExceeded riporta se il controller ha gia'
	// concluso, per conto proprio, che il rollout non progredisce entro
	// il proprio deadline. ok=false significa "non applicabile a questo
	// tipo di controller o non ancora superato", mai "sicuramente non
	// superato": i chiamanti non devono confondere le due cose.
	ProgressDeadlineExceeded() (message string, ok bool)
}

type deploymentWorkload struct {
	d *appsv1.Deployment
}

// FromDeployment costruisce un Workload a partire da un Deployment live.
func FromDeployment(d *appsv1.Deployment) Workload {
	return &deploymentWorkload{d: d}
}

func (w *deploymentWorkload) Kind() string      { return "Deployment" }
func (w *deploymentWorkload) Name() string      { return w.d.Name }
func (w *deploymentWorkload) Namespace() string { return w.d.Namespace }
func (w *deploymentWorkload) UID() types.UID    { return w.d.UID }

func (w *deploymentWorkload) Replicas() int32 {
	if w.d.Spec.Replicas == nil {
		return 1
	}
	return *w.d.Spec.Replicas
}

func (w *deploymentWorkload) PodLabels() map[string]string {
	return w.d.Spec.Template.Labels
}

func (w *deploymentWorkload) PodSelector() (labels.Selector, error) {
	return metav1.LabelSelectorAsSelector(w.d.Spec.Selector)
}

func (w *deploymentWorkload) UpdateStrategy() UpdateStrategy {
	strategyType := w.d.Spec.Strategy.Type
	if strategyType == "" {
		strategyType = RollingUpdate
	}
	if strategyType == appsv1.RecreateDeploymentStrategyType {
		return UpdateStrategy{Type: Recreate}
	}

	ru := w.d.Spec.Strategy.RollingUpdate
	maxUnavailable := intstr.FromString(defaultMaxUnav)
	maxSurge := intstr.FromString(defaultMaxSurge)
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

// PodRequests implementa Workload.
func (w *deploymentWorkload) PodRequests() corev1.ResourceList {
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

// RolloutComplete replica la logica di `kubectl rollout status` per
// Deployment (pkg/polymorphichelpers/rollout_status.go): la Spec
// corrente deve essere stata osservata dal controller, le repliche
// aggiornate devono aver raggiunto il numero desiderato, non devono
// restare repliche vecchie in attesa di terminazione, e le repliche
// aggiornate devono essere tutte disponibili. Replicare lo stesso
// calcolo di un tool che l'ecosistema gia' considera corretto evita di
// reinventare una definizione di "successo" leggermente diversa e
// difficile da giustificare.
func (w *deploymentWorkload) RolloutComplete() bool {
	d := w.d
	if d.Generation > d.Status.ObservedGeneration {
		return false
	}
	desired := w.Replicas()
	if d.Status.UpdatedReplicas < desired {
		return false
	}
	if d.Status.Replicas > d.Status.UpdatedReplicas {
		return false
	}
	if d.Status.AvailableReplicas < d.Status.UpdatedReplicas {
		return false
	}
	return true
}

// progressDeadlineExceededReason e' il valore di Reason che il
// controller del Deployment scrive sulla condition "Progressing"
// quando supera spec.progressDeadlineSeconds (deploymentutil.
// TimedOutReason in k8s.io/kubernetes, non importabile qui: e' un
// letterale stabile, parte del contratto osservabile del controller,
// non un messaggio di log soggetto a riformulazione).
const progressDeadlineExceededReason = "ProgressDeadlineExceeded"

// ProgressDeadlineExceeded legge la condition "Progressing" dallo
// Status del Deployment: e' il controller stesso ad aver gia' concluso
// che il rollout e' bloccato, non e' una deduzione di questo progetto
// da un timeout proprio.
func (w *deploymentWorkload) ProgressDeadlineExceeded() (message string, ok bool) {
	for _, c := range w.d.Status.Conditions {
		if c.Type == appsv1.DeploymentProgressing && c.Reason == progressDeadlineExceededReason {
			return c.Message, true
		}
	}
	return "", false
}
