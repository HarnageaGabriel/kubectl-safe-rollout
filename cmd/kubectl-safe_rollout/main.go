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

// Command kubectl-safe_rollout e' l'entrypoint del plugin krew
// "safe-rollout". Il nome del binario segue la convenzione krew per i
// comandi multi-parola (underscore al posto del trattino); l'help e i
// messaggi lo presentano comunque come "kubectl safe-rollout".
package main

import (
	"errors"
	"fmt"
	"os"
)

// errHighSeverity e' un errore sentinella: segnala a main che deve
// uscire con codice non zero perche' sono stati trovati finding di
// severita' alta, non perche' l'esecuzione e' fallita. La distinzione
// conta perche' in quel caso l'output (gia' stampato dal comando) e' il
// messaggio, e ripeterlo come "errore" su stderr sarebbe fuorviante.
var errHighSeverity = errors.New("trovati finding di severita' alta")

func main() {
	err := newRootCommand().Execute()
	if err == nil {
		return
	}
	if !errors.Is(err, errHighSeverity) {
		fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(1)
}
