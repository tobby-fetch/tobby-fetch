// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"fmt"
	"log/slog"

	"github.com/spf13/cobra"

	"github.com/tobby-fetch/tobby-fetch/internal/buildinfo"
	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/consume"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
)

// Report formats of the mapping table, on top of the shared outputJSON.
const (
	outputMarkdown = "markdown"
	outputCSV      = "csv"
)

// newConsumptionCmd wires `tobby consumption` (FR-065, 7.1): the aids a
// cluster needs to consume what a zone registry holds. Both read the
// recipe graph of the store directory, like `tobby store verify`, so they
// work on a host where no instance is running — the destination side of a
// transported store.
func newConsumptionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "consumption",
		Short: "Generate the consumption aids: mapping table and registries.yaml (FR-065)",
		Long: `Generate what a cluster in the zone needs to pull what Tobby relocated.

Both aids are computed from the recipes the store holds, for one registry:
consumption.registry when set, destination.registry otherwise, or the
address given with --destination. Neither carries a credential or a
timestamp, and neither is ever written to a node.`,
	}
	cmd.AddCommand(newConsumptionTableCmd(), newConsumptionRegistriesCmd())
	return cmd
}

// consumptionFlags are the selection shared by both aids.
type consumptionFlags struct {
	common      commonFlags
	recipes     []string
	destination string
}

func (f *consumptionFlags) register(cmd *cobra.Command) {
	f.common.register(cmd)
	cmd.Flags().StringArrayVar(&f.recipes, "recipe", nil,
		"recipe to cover, name or name@version (repeatable; default: every recipe the store holds)")
	cmd.Flags().StringVar(&f.destination, "destination", "",
		"registry host the zone pulls from, host or host:port (default: consumption.registry, then destination.registry)")
}

// service opens the store directory and resolves the settings.
func (f *consumptionFlags) service(cmd *cobra.Command) (*consume.Service, []consume.Selector, func(), error) {
	cfg, err := f.common.loadFor(cmd, config.ScopeStorage)
	if err != nil {
		return nil, nil, nil, err
	}
	sel := make([]consume.Selector, 0, len(f.recipes))
	for _, r := range f.recipes {
		s, err := consume.ParseSelector(r)
		if err != nil {
			return nil, nil, nil, &usageError{err: fmt.Errorf("--recipe: %w", err), hint: "see '" + cmd.CommandPath() + " --help'"}
		}
		sel = append(sel, s)
	}
	set := consume.FromConfig(&cfg, buildinfo.Version())
	if f.destination != "" {
		if err := config.CheckRegistryHost("--destination", f.destination); err != nil {
			return nil, nil, nil, &usageError{err: err, hint: "see '" + cmd.CommandPath() + " --help'"}
		}
		set = set.WithRegistry(f.destination, cfg.Registries.Insecure)
	}
	st, err := store.Open(cmd.Context(), cfg.Storage.Root, slog.New(slog.DiscardHandler))
	if err != nil {
		return nil, nil, nil, err
	}
	return consume.New(st, set), sel, func() { _ = st.Close() }, nil
}

func newConsumptionTableCmd() *cobra.Command {
	var flags consumptionFlags
	report := newReportFlag(outputMarkdown, outputJSON, outputCSV)
	cmd := &cobra.Command{
		Use:   "table",
		Short: "Print the source→destination mapping table",
		Long: `Print the table that maps every ingredient of the selected recipes from the
reference its recipe names to the reference it has at the destination
registry, with its pinned digest. FileSets are listed like images.

The table is computed from what the store holds, so it matches what was
pushed. --output chooses the format: markdown (the default), json or csv;
all three carry the same rows. In JSON the document is
{"destination": ..., "entries": [...]}.

  tobby consumption table --recipe wordpress@6.5.2 --output csv > mapping.csv`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := report.validate(cmd); err != nil {
				return err
			}
			svc, sel, done, err := flags.service(cmd)
			if err != nil {
				return err
			}
			defer done()
			table, err := svc.Table(sel)
			if err != nil {
				return err
			}
			out, err := table.Render(consume.Format(report.value))
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(out)
			return err
		},
	}
	flags.register(cmd)
	report.register(cmd)
	return cmd
}

func newConsumptionRegistriesCmd() *cobra.Command {
	var flags consumptionFlags
	cmd := &cobra.Command{
		Use:     "registries-yaml",
		Aliases: []string{"registries"},
		Short:   "Print the K3s/RKE2 registries.yaml mirror snippet",
		Long: `Print a registries.yaml for K3s and RKE2 that redirects the image pulls of the
nodes' container runtime to the zone registry: one mirror per source registry
host of the selected recipes' container images, whose endpoint is the
destination (http when it is listed in registries.insecure, https otherwise)
and whose rewrite maps each repository onto its relocated path (ADR-0013).

The snippet never contains a credential; the CA file declared in
consumption.caFile, a path on the nodes, is the only configs entry. Its
header states the limits: only the container runtime of the node is
redirected — chart sources, GitOps controllers and admission policies must
use the relocated paths of the mapping table.

Install the output on every node as /etc/rancher/k3s/registries.yaml or
/etc/rancher/rke2/registries.yaml and restart the service. The output is
deterministic: the same store and configuration yield the same bytes.

  tobby consumption registries-yaml --recipe rke2@1.36.5 --destination 10.0.0.5:5000`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			svc, sel, done, err := flags.service(cmd)
			if err != nil {
				return err
			}
			defer done()
			out, err := svc.RegistriesYAML(sel)
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(out)
			return err
		},
	}
	flags.register(cmd)
	return cmd
}
