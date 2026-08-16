// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/engine"
)

// newRecipeCmd wires `tobby recipe` (R-36): authoring-side commands that
// talk to a cookbook without needing a serving instance.
func newRecipeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "recipe",
		Short: "Work with recipe documents and cookbooks (R-36)",
	}
	cmd.AddCommand(newRecipePushCmd())
	return cmd
}

func newRecipePushCmd() *cobra.Command {
	flags := &commonFlags{}
	cmd := &cobra.Command{
		Use:   "push <file> <registry>/<cookbook>/<name>:<version>",
		Short: "Validate a recipe and publish it to a cookbook",
		Long: `Publish a recipe document as the OCI artifact the specification defines,
after checking it — which is the difference with a generic push tool.

The publication is refused when the document is not a valid Recipe, when it
is not fully pinned (a cookbook holds cooked recipes only: every ingredient
carries an exact tag and a digest), when its name or version contradicts the
reference it is being published under, or when the tag already exists with
different content — a published recipe version is immutable.

Publishing the same document twice is a no-op, not a conflict.

Signing stays outside Tobby, which never holds a private key. The published
digest is printed on stdout, ready for cosign:

  tobby recipe push harbor.yaml registry.example.com/cookbook/harbor:2.15.2
  cosign sign --key cosign.key --use-signing-config=false --tlog-upload=false \
    registry.example.com/cookbook/harbor@<the printed digest>`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			file, ref := args[0], args[1]
			doc, err := os.ReadFile(file) //nolint:gosec // G304: publishing the operator's own file is the feature
			if err != nil {
				return err
			}
			// Registry-facing scope: credentials and insecure hosts, no
			// mode — publishing says nothing about how this host serves
			// (R-34, B-006).
			cfg, err := flags.loadFor(cmd, config.ScopeRegistries)
			if err != nil {
				return err
			}
			publisher, err := engine.NewPublisher(cfg.Registries.Insecure, cfg.Registries.CredentialsFile)
			if err != nil {
				return err
			}
			res, err := publisher.PublishRecipe(cmd.Context(), ref, doc)
			if err != nil {
				return err
			}

			// Human feedback on stderr, the digest alone on stdout: the
			// command composes into a signing pipeline (B-010).
			what := "published"
			if res.Unchanged {
				what = "already published, unchanged"
			}
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "%s %s\n", res.Reference, what)
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "sign it with:\n  cosign sign --key <key> --use-signing-config=false --tlog-upload=false %s@%s\n",
				repositoryOf(res.Reference), res.Digest)
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), res.Digest)
			return nil
		},
	}
	flags.register(cmd)
	return cmd
}

// repositoryOf strips the tag from a published reference, so the printed
// cosign line targets the digest — cosign signs digests, never tags.
func repositoryOf(ref string) string {
	if i := strings.LastIndexByte(ref, ':'); i > strings.LastIndexByte(ref, '/') {
		return ref[:i]
	}
	return ref
}
