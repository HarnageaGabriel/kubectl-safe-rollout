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
	"io"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/check"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/kube"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/output"
)

// registeredChecks lists the checks run by `check`. It is the only place
// to update when adding a new check to the command: each Check already
// degrades on its own (Result.Skipped) when a resource it needs is
// unavailable.
func registeredChecks() []check.Check {
	return []check.Check{
		check.PDBConsistency{},
		check.QuotaHeadroom{},
		check.ServiceAccountExists{},
		check.ServiceRouting{},
		check.IngressRouting{},
		check.ConfigReferencesExist{},
		check.NetworkPolicyIngress{},
		check.RequestsVsUsage{},
		check.ProbeSanity{},
		check.ResourceLimits{},
		check.ImagePullSecrets{},
	}
}

func newCheckCommand(configFlags *genericclioptions.ConfigFlags) *cobra.Command {
	var outputFormat string

	cmd := &cobra.Command{
		Use:   "check <kind>/<name>",
		Short: "Run a static pre-flight analysis of a workload before a rollout",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCheck(cmd, configFlags, args[0], outputFormat)
		},
	}

	cmd.Flags().StringVar(&outputFormat, "output", "human", `output format: "human" or "json"`)

	return cmd
}

func runCheck(cmd *cobra.Command, configFlags *genericclioptions.ConfigFlags, ref, outputFormat string) error {
	if err := validateOutputFormat(outputFormat); err != nil {
		return err
	}

	ctx := cmd.Context()

	clients, err := kube.NewClients(configFlags)
	if err != nil {
		return err
	}

	namespace := clients.Namespace
	if ns := *configFlags.Namespace; ns != "" {
		namespace = ns
	}

	wl, err := kube.ResolveWorkload(ctx, clients.Clientset, namespace, ref)
	if err != nil {
		return err
	}

	target := check.Target{
		Namespace:     namespace,
		Workload:      wl,
		Client:        clients.Clientset,
		MetricsClient: nil,
	}
	if clients.Metrics != nil {
		target.MetricsClient = clients.Metrics.MetricsV1beta1()
	}

	groups := make([]output.Group, 0, len(registeredChecks()))
	for _, c := range registeredChecks() {
		res, err := c.Run(ctx, target)
		if err != nil {
			return fmt.Errorf("check %s: %w", c.ID(), err)
		}
		groups = append(groups, output.Group{
			ID:         res.CheckID,
			Findings:   res.Findings,
			Skipped:    res.Skipped,
			SkipReason: res.SkipReason,
		})
	}

	report := output.NewReport(groups)

	return renderCheckReport(cmd.OutOrStdout(), report, outputFormat)
}

func validateOutputFormat(outputFormat string) error {
	if outputFormat != "human" && outputFormat != "json" {
		return fmt.Errorf("invalid --output: %q (expected human or json)", outputFormat)
	}
	return nil
}

func renderCheckReport(w io.Writer, report output.Report, outputFormat string) error {
	var renderErr error
	if outputFormat == "json" {
		renderErr = output.RenderJSON(w, report)
	} else {
		renderErr = output.RenderHuman(w, report)
	}
	if renderErr != nil {
		return renderErr
	}

	if sev, ok := report.MaxSeverity(); ok && sev == model.SeverityHigh {
		return errHighSeverity
	}
	return nil
}
