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

package main

import (
	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"
)

func newRootCommand() *cobra.Command {
	configFlags := genericclioptions.NewConfigFlags(true)

	root := &cobra.Command{
		Use:   "safe-rollout",
		Short: "Riduce il tempo tra \"il deploy e' fallito\" e \"so perche' e come lo sistemo\"",
		Long: `kubectl safe-rollout analizza un workload prima e durante un rollout per
individuare le cause ricorrenti di rollout falliti (PodDisruptionBudget
incoerenti, probe assenti o aggressive, risorse insufficienti, quota
esaurita, immagini non tirabili) e propone una remediation concreta.

Non modifica mai lo stato del cluster.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	configFlags.AddFlags(root.PersistentFlags())

	root.AddCommand(newCheckCommand(configFlags))
	root.AddCommand(newWatchCommand(configFlags))

	return root
}
