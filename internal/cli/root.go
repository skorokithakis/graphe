// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"fmt"
	"os"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// version is the baseline release version. The actual reported value also
// includes VCS metadata from runtime/debug when the binary was built from a
// git checkout (commit short-hash and a "dirty" marker for uncommitted edits).
const version = "0.1.0"

var rootCommand = &cobra.Command{
	Use:   "graphe",
	Short: "Render a markdown post in the browser with margin-note review comments.",
	Long: `graphe renders a markdown file in the browser as a Tufte-style post and
overlays reviewer comments as margin notes. It is designed for human-LLM
collaborative review of prose drafts.

Run 'graphe help' for a full overview, including how an LLM should use it.`,
	Version: buildVersion(),
}

// buildVersion appends VCS info to the baseline version string when available.
func buildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return version
	}
	var revision, modified string
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			if len(setting.Value) >= 7 {
				revision = setting.Value[:7]
			}
		case "vcs.modified":
			if setting.Value == "true" {
				modified = "-dirty"
			}
		}
	}
	if revision == "" {
		return version
	}
	return fmt.Sprintf("%s (%s%s)", version, revision, modified)
}

// Execute runs the root command and exits on error.
func Execute() {
	if err := rootCommand.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
