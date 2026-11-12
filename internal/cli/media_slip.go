// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/tobby-fetch/tobby-fetch/internal/buildinfo"
	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/engine"
	"github.com/tobby-fetch/tobby-fetch/internal/slip"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// outputHTML is the self-contained HTML rendering of `tobby media slip`.
const outputHTML = "html"

// newMediaSlipCmd wires `tobby media slip` (R-07): the transfer slip, drawn
// on the host that holds the medium, with no serving instance needed.
func newMediaSlipCmd() *cobra.Command {
	flags := &mediaFlags{report: newReportFlag(outputText, outputHTML, outputJSON)}
	var lang string
	cmd := &cobra.Command{
		Use:   "slip",
		Short: "Draw the printable transfer slip of a store (R-07)",
		Long: `Draw the transfer slip: the printable, bilingual account of one physical
transfer — the medium's identity, freshness and inventory, the verification
report, the vulnerability-scan reports per recipe and element with the age of
the database and the mode, and the reduced-assurance markers.

With a zone identity (--zone, the configuration) the command is the
destination side: it verifies the medium first, exactly as 'tobby media
verify' does and writing nothing, and the slip carries the verdict. Without
one it is the source side and summarizes what the store delivers. A store
holding no manifest still gets a slip, which says so.

--output text (the default) prints the plain-text document, html a
self-contained page whose print rules are inline and which loads nothing,
json the language-neutral document. --lang picks English or French (default:
the LC_ALL / LANG environment).

The slip is UNSIGNED and says so at its head and its foot. It is an aid, not
a proof of authenticity: Tobby signs nothing (ADR-0006). It exits 0 whatever
the verdict — a blocked medium is a successful slip of a bad medium.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runMediaSlip(cmd, flags, lang)
		},
	}
	flags.register(cmd)
	cmd.Flags().StringVar(&lang, "lang", "", "language of the slip: \"en\" or \"fr\" (default: from LC_ALL / LANG)")
	return cmd
}

func runMediaSlip(cmd *cobra.Command, flags *mediaFlags, lang string) error {
	if err := flags.report.validate(cmd); err != nil {
		return err
	}
	switch lang {
	case "":
		lang = langFromEnvironment()
	case "en", "fr":
	default:
		return &usageError{
			err:  fmt.Errorf(`--lang %q: expected "en" or "fr"`, lang),
			hint: "see '" + cmd.CommandPath() + " --help'",
		}
	}
	cfg, err := flags.common.loadFor(cmd, config.ScopeMedia)
	if err != nil {
		return err
	}
	if flags.zone != "" {
		cfg.Zone = flags.zone
	}
	if cfg.Storage.Root == "" {
		return taxonomy.New(taxonomy.CodeConfigInvalid, taxonomy.Params{
			"detail": "no store to read: set --storage-root, " + config.EnvStorageRoot +
				", or \"storage.root:\" in the configuration file",
		})
	}

	stored, err := tasks.ReadAll(cfg.Storage.Root)
	if err != nil {
		return err
	}
	inst := &slip.Instance{
		Version: buildinfo.Version(), Zone: cfg.Zone, Root: cfg.Storage.Root, Tasks: stored,
		// No scanner runs in this process: the policy is the
		// configured one, and the database in use is not read.
		Scan: func() slip.ScanContext {
			return slip.ScanContext{Blocking: cfg.Scan.Mode == config.ScanModeBlock, Unknown: true}
		},
	}

	// A destination side verifies first — the slip is worth handing over
	// WITH the verdict. A store without a manifest has nothing to verify:
	// the slip says so instead of failing.
	if cfg.Zone != "" {
		if m, _, _ := slip.LoadMedium(cfg.Storage.Root, true); m != nil {
			op, err := openMedia(cmd.Context(), &cfg, cmd.ErrOrStderr(), verifyOnly)
			if err != nil {
				return err
			}
			defer op.close()
			rep, err := op.eng.VerifyMedia(cmd.Context(), op.logger, engine.MediaOptions{
				AllowZoneMismatch: flags.allowZoneMismatch,
				AllowStale:        flags.allowStale,
			})
			if err != nil {
				return err
			}
			inst.Report = rep
			if last := op.eng.MediaSummary().LastImport; last != nil {
				inst.LastImport = last
			}
		}
	}

	sl := inst.Slip()
	format := flags.report.value
	body, _, err := sl.Render(map[string]string{outputText: slip.FormatText, outputHTML: slip.FormatHTML, outputJSON: slip.FormatJSON}[format], lang)
	if err != nil {
		return err
	}
	_, err = cmd.OutOrStdout().Write(body)
	return err
}

// langFromEnvironment picks the slip's language from the locale variables,
// in the order a C library reads them; anything but French is English.
func langFromEnvironment() string {
	for _, name := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := os.Getenv(name); v != "" {
			if strings.HasPrefix(strings.ToLower(v), "fr") {
				return "fr"
			}
			return "en"
		}
	}
	return "en"
}
