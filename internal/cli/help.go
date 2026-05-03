// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	_ "embed"
	"fmt"

	"github.com/spf13/cobra"
)

//go:embed help.txt
var overviewText string

func init() {
	// Override cobra's built-in help command so that 'graphe help' (no args)
	// prints the LLM-oriented prose overview. When called with a subcommand
	// name ('graphe help comment'), we fall through to cobra's default
	// per-command usage output, preserving the expected behaviour.
	rootCommand.SetHelpCommand(helpCommand)
}

var helpCommand = &cobra.Command{
	Use:   "help [command]",
	Short: "Print an overview of graphe, or usage for a specific command.",
	Long: `With no arguments, prints a long-form overview designed to give an LLM
everything it needs to perform a review. With a command name, prints that
command's usage (same as 'graphe <command> --help').`,
	// DisableFlagParsing lets us forward arbitrary args to cobra's help func
	// without cobra complaining about unknown flags.
	DisableFlagParsing: true,
	Run: func(cmd *cobra.Command, args []string) {
		// Filter out any --help/-h flags that cobra may have injected.
		filtered := make([]string, 0, len(args))
		for _, argument := range args {
			if argument != "--help" && argument != "-h" {
				filtered = append(filtered, argument)
			}
		}

		if len(filtered) == 0 {
			fmt.Print(overviewText)
			return
		}

		// Delegate to cobra's built-in help for the named subcommand.
		target, remainingArgs, err := rootCommand.Find(filtered)
		if err != nil || target == rootCommand {
			// Unknown subcommand — let cobra print the error.
			_ = remainingArgs
			fmt.Printf("unknown command %q for graphe\n\nRun 'graphe help' for an overview.\n", filtered[0])
			return
		}
		target.HelpFunc()(target, remainingArgs)
	},
}
