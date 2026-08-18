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

// Package model contains the types shared by check, diagnose, remediate,
// and output. Representing a Finding the same way regardless of whether it
// comes from static analysis (check) or a live rollout diagnosis (diagnose)
// allows the output layer to render them with the same code.
package model

import (
	"encoding/json"
	"fmt"
)

// Severity indicates how much a Finding blocks a safe rollout. Value order
// is significant: a higher Severity has a greater integer value, so a simple
// numeric comparison is enough to determine the highest exit code to return
// in CI.
type Severity int

const (
	// SeverityLow reports a deviation from best practices that does not
	// put the current rollout at risk (e.g. missing limits but requests
	// are present).
	SeverityLow Severity = iota
	// SeverityMedium reports a concrete but nondeterministic risk: the
	// rollout may fail depending on cluster conditions.
	SeverityMedium
	// SeverityHigh reports a condition that, with high confidence, will
	// block or has already blocked the rollout.
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

// MarshalJSON serializes Severity as a lowercase string rather than an
// integer, keeping JSON output readable and stable across versions even if
// the constant order changes in the future.
func (s Severity) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf("%q", s.String())), nil
}

// UnmarshalJSON is the counterpart to MarshalJSON: it serves consumers of
// `check` JSON output (e.g. in CI or a future library) and this package's
// round-trip tests.
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
		return fmt.Errorf("unknown severity: %q", str)
	}
	return nil
}

// ResourceRef identifies the Kubernetes resource a Finding refers to. It is
// separate from the rest of the Finding because a single check may, in the
// future, produce findings for resources other than the main target (e.g.
// the PDB check emits a finding for the PodDisruptionBudget, not the
// Deployment).
type ResourceRef struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

func (r ResourceRef) String() string {
	return fmt.Sprintf("%s/%s", r.Kind, r.Name)
}

// Remediation describes how to address a Finding. ContextDependent must be
// true whenever the suggested command might not be correct or safe without
// operator judgment: the project's quality criteria prohibit a Finding
// without concrete remediation OR without this explicit declaration. They
// do not allow a third case where something potentially wrong is suggested
// as safe.
type Remediation struct {
	// Summary is the natural-language explanation of the suggested action.
	// Required.
	Summary string `json:"summary"`
	// Commands contains ready-to-use kubectl commands when there is an
	// unambiguous action. It may be empty if ContextDependent is true.
	Commands []string `json:"commands,omitempty"`
	// ContextDependent declares that the correct remediation depends on
	// choices only the operator can make (e.g. which replicas value is
	// acceptable for the business).
	ContextDependent bool `json:"contextDependent"`
}

// Finding is the atomic reporting unit, produced by both a Check (pre-flight
// static analysis) and a Diagnoser (failed rollout classification). CheckID
// is the stable identifier of the check or classification that generated
// the finding (e.g. "pdb-consistency", "crashloop-oomkilled"): it is what
// the JSON output exposes to allow CI gating on individual rules.
type Finding struct {
	CheckID     string      `json:"checkId"`
	Severity    Severity    `json:"severity"`
	Cause       string      `json:"cause"`
	Evidence    []string    `json:"evidence,omitempty"`
	Remediation Remediation `json:"remediation"`
	Resource    ResourceRef `json:"resource"`
	// Undetermined is false by construction in every Finding produced by
	// a Check (static analysis is deterministic by definition: it either
	// finds the condition or does not). A Diagnoser sets it to true when
	// evidence collected during a watch confirms that the rollout is
	// blocked but is insufficient to distinguish among known causes: in
	// that case Cause describes "undetermined" and Evidence lists what
	// was observed, never a guessed cause: a wrong suggestion on a
	// production cluster costs more than silence.
	Undetermined bool `json:"undetermined,omitempty"`
}
