// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCommand = &cobra.Command{
	Use:   "graphe",
	Short: "Render a markdown post in the browser with margin-note review comments.",
	Long: `graphe renders a markdown file in the browser as a Tufte-style post and
overlays reviewer comments as margin notes. It is designed for human-LLM
collaborative review of prose drafts.

Run 'graphe help' for a full overview, including how an LLM should use it.`,
}

// Execute runs the root command and exits on error.
func Execute() {
	if err := rootCommand.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
