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
	"fmt"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/diagnose"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/kube"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/output"
)

func newWatchCommand(configFlags *genericclioptions.ConfigFlags) *cobra.Command {
	var outputFormat string
	cmd := &cobra.Command{
		Use:   "watch <kind>/<name>",
		Short: "Osserva un rollout in corso e classifica la causa se fallisce",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWatch(cmd, configFlags, args[0], outputFormat)
		},
	}
	cmd.Flags().StringVar(&outputFormat, "output", "human", `formato di output: "human" o "json"`)
	return cmd
}

func runWatch(cmd *cobra.Command, configFlags *genericclioptions.ConfigFlags, ref, outputFormat string) error {
	if outputFormat != "human" && outputFormat != "json" {
		return fmt.Errorf("--output non valido: %q (atteso human o json)", outputFormat)
	}

	clients, err := kube.NewClients(configFlags)
	if err != nil {
		return err
	}
	namespace := clients.Namespace
	if ns := *configFlags.Namespace; ns != "" {
		namespace = ns
	}

	wl, err := kube.ResolveWorkload(cmd.Context(), clients.Clientset, namespace, ref)
	if err != nil {
		return err
	}
	outcome, err := diagnose.Watch(cmd.Context(), diagnose.WatchTarget{
		Namespace: namespace,
		Workload:  wl,
		Client:    clients.Clientset,
		LogTailer: kube.PreviousLogTailer{Client: clients.Clientset},
	})
	if err != nil {
		return err
	}
	groups := make([]output.Group, 0, len(outcome.Results))
	for _, result := range outcome.Results {
		groups = append(groups, output.Group{
			ID:         result.DiagnoserID,
			Findings:   result.Findings,
			Skipped:    result.Skipped,
			SkipReason: result.SkipReason,
		})
	}
	report := output.NewReport(groups)
	if outcome.Succeeded {
		report.Status = "succeeded"
	}
	if outputFormat == "json" {
		err = output.RenderJSON(cmd.OutOrStdout(), report)
	} else {
		err = output.RenderHuman(cmd.OutOrStdout(), report)
	}
	if err != nil {
		return err
	}
	if severity, ok := report.MaxSeverity(); ok && severity == model.SeverityHigh {
		return errHighSeverity
	}
	return nil
}
