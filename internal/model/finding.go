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

// Package model contiene i tipi condivisi tra check, diagnose, remediate e
// output. Un Finding rappresentato allo stesso modo indipendentemente dal
// fatto che provenga da un'analisi statica (check) o da una diagnosi di un
// rollout live (diagnose) permette all'output layer di renderli con lo
// stesso codice.
package model

import (
	"encoding/json"
	"fmt"
)

// Severity indica quanto un Finding e' bloccante per un rollout sicuro.
// L'ordine dei valori e' significativo: Severity piu' alta ha valore
// intero maggiore, cosi' un semplice confronto numerico basta per
// determinare l'exit code piu' alto da restituire in CI.
type Severity int

const (
	// SeverityLow segnala una deviazione dalle best practice che non
	// mette a rischio il rollout in corso (es. limits assenti ma
	// requests presenti).
	SeverityLow Severity = iota
	// SeverityMedium segnala un rischio concreto ma non deterministico:
	// il rollout puo' fallire a seconda delle condizioni del cluster.
	SeverityMedium
	// SeverityHigh segnala una condizione che, con alta confidenza,
	// blocchera' o ha gia' bloccato il rollout.
	SeverityHigh
)

func (s Severity) String() string {
	switch s {
	case SeverityLow:
		return "low"
	case SeverityMedium:
		return "medium"
	case SeverityHigh:
		return "high"
	default:
		return "unknown"
	}
}

// MarshalJSON serializza la Severity come stringa minuscola invece che
// come intero, per un output JSON leggibile e stabile tra versioni anche
// se l'ordine delle costanti cambiasse in futuro.
func (s Severity) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf("%q", s.String())), nil
}

// UnmarshalJSON e' il contraltare di MarshalJSON: serve a chi consuma
// l'output json di `check` (es. in CI, o una futura libreria) e ai test
// di round-trip di questo stesso package.
func (s *Severity) UnmarshalJSON(data []byte) error {
	var str string
	if err := json.Unmarshal(data, &str); err != nil {
		return err
	}
	switch str {
	case "low":
		*s = SeverityLow
	case "medium":
		*s = SeverityMedium
	case "high":
		*s = SeverityHigh
	default:
		return fmt.Errorf("severity sconosciuta: %q", str)
	}
	return nil
}

// ResourceRef identifica la risorsa Kubernetes a cui un Finding si
// riferisce. E' separato dal resto del Finding perche' un singolo check
// puo', in futuro, produrre finding su risorse diverse dal target
// principale (es. il check PDB emette un finding sul PodDisruptionBudget,
// non sul Deployment).
type ResourceRef struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

func (r ResourceRef) String() string {
	return fmt.Sprintf("%s/%s", r.Kind, r.Name)
}

// Remediation descrive come intervenire su un Finding. ContextDependent
// deve essere true ogni volta che il comando suggerito potrebbe non
// essere corretto o sicuro senza il giudizio di chi opera: i criteri di
// qualita' del progetto vietano un Finding senza remediation concreta O
// senza questa dichiarazione esplicita, non ammettono un terzo caso in
// cui si suggerisce qualcosa di potenzialmente sbagliato spacciandolo
// per sicuro.
type Remediation struct {
	// Summary e' la spiegazione in linguaggio naturale dell'intervento
	// suggerito. Obbligatorio.
	Summary string `json:"summary"`
	// Commands sono comandi kubectl pronti per l'uso, quando esiste un
	// intervento univoco. Puo' essere vuoto se ContextDependent e' true.
	Commands []string `json:"commands,omitempty"`
	// ContextDependent dichiara che la remediation corretta dipende da
	// scelte che solo chi opera puo' fare (es. quale valore di replicas
	// e' accettabile per il business).
	ContextDependent bool `json:"contextDependent"`
}

// Finding e' l'unita' atomica di segnalazione, prodotta sia da un Check
// (analisi statica pre-flight) sia da un Diagnoser (classificazione di un
// rollout fallito). CheckID e' l'identificativo stabile della verifica o
// della classificazione che ha generato il finding (es.
// "pdb-consistency", "crashloop-oomkilled"): e' quello che l'output json
// espone per permettere il gating in CI su singole regole.
type Finding struct {
	CheckID     string      `json:"checkId"`
	Severity    Severity    `json:"severity"`
	Cause       string      `json:"cause"`
	Evidence    []string    `json:"evidence,omitempty"`
	Remediation Remediation `json:"remediation"`
	Resource    ResourceRef `json:"resource"`
	// Undetermined e' false per costruzione in ogni Finding prodotto da
	// un Check (l'analisi statica e' deterministica per definizione: o
	// trova la condizione o non la trova). Un Diagnoser lo imposta a
	// true quando l'evidenza raccolta durante un watch conferma che il
	// rollout e' bloccato ma non basta a distinguere tra le cause note:
	// in quel caso Cause descrive "non determinato" ed Evidence elenca
	// cosa e' stato osservato, mai una causa scelta a indovinare:
	// un suggerimento sbagliato su un cluster di produzione costa piu'
	// del silenzio.
	Undetermined bool `json:"undetermined,omitempty"`
}
