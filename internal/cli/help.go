// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

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

// overviewText is the LLM-oriented prose document printed by 'graphe help'.
// It is intentionally long-form: an LLM reading only this output should be
// able to add well-formed comments to a markdown file without any other
// context.
const overviewText = `graphe — a local prose-review tool for human-LLM collaboration
===============================================================

WHAT IT IS

graphe renders a markdown post in the browser as a Tufte-style page and
overlays reviewer comments as margin notes. It is designed for the workflow
where a human author asks an LLM to review a draft: the human runs the server,
the LLM adds structured comments via the CLI, and the human reads the rendered
page to decide which feedback to act on.

Comments are stored in a sidecar JSON file (<stem>-review.json) that lives
next to the markdown file. The browser page reloads automatically whenever
either file changes.


WHEN TO USE IT

Use graphe when you want structured, anchored feedback on a prose draft — not
inline edits, but margin notes that point to specific passages. The typical
session looks like this:

  1. The human opens a terminal and runs:
       graphe serve path/to/post.md
     The post appears in the browser at http://127.0.0.1:7290.

  2. The human asks an LLM to review the post and add comments.

  3. The LLM reads the markdown file, then calls 'graphe comment add' for each
     piece of feedback (see "The review workflow" below).

  4. The browser reloads automatically. The human reads the margin notes and
     resolves each one by running 'graphe comment delete'.


THE REVIEW WORKFLOW, END TO END

From the human's point of view:
  1. Run 'graphe serve post.md'.
  2. Ask the LLM: "Read post.md and add review comments using graphe."
  3. Read the rendered page. Each comment appears as a margin note next to the
     passage it references.
  4. Delete comments you have acted on:
       graphe comment delete post.md <id>

From the LLM's point of view:
  1. Read the markdown file (the raw source, not the rendered HTML).
  2. Identify passages that need feedback.
  3. For each passage, choose a short unique substring that marks where the
     comment should start, and another that marks where it ends (they may be
     the same string for a single-sentence highlight).
  4. Run:
       graphe comment add post.md --start "..." --end "..." --body "..."
     The command prints the new comment ID on success (e.g. "c-a1b2").
  5. Run 'graphe comment list post.md' to verify all comments look correct.


HOW COMMENTS ANCHOR TO TEXT

Each comment is anchored by two substrings of the post body (the markdown
source after the YAML (---) or TOML (+++) frontmatter is stripped):

  --start   A substring that marks the beginning of the highlighted passage.
  --end     A substring that marks the end of the highlighted passage.

Both substrings must appear exactly once in the document. If a substring
matches more than one place, the command exits with an error:

  error: start anchor matches 3 places; make it more specific

To fix this, include a few more surrounding words until the substring is
unique. Start and end may be identical when you want to highlight a single
sentence or phrase.

Worked example — given this passage in the post:

  "The proposal has three main weaknesses. First, the timeline is optimistic.
  Second, the budget does not account for contingencies."

A comment on the whole passage:
  --start "The proposal has three main weaknesses"
  --end   "account for contingencies."

A comment on just the second sentence:
  --start "First, the timeline is optimistic."
  --end   "First, the timeline is optimistic."

The anchor text is matched literally (case-sensitive, whitespace-sensitive).
Do not include markdown formatting characters in the anchor unless they appear
verbatim in the source.

Do not anchor inside fenced code blocks (triple-backtick or triple-tilde
fences): the highlight will not render because goldmark HTML-escapes the
inserted <mark> tag.


COMMAND REFERENCE

  graphe serve <file.md> [--host HOST] [--port PORT]
    Start the browser preview server. The page reloads on every save.
    Defaults to 127.0.0.1:7290.
    Examples:
      graphe serve drafts/essay.md
      graphe serve drafts/essay.md --port 8080
      graphe serve drafts/essay.md --host 0.0.0.0 --port 7290

  graphe comment add <file.md> --start "..." --end "..." --body "..."
    Add a new comment anchored to the range [start, end]. Prints the new ID.
    Example:
      graphe comment add post.md \
        --start "The timeline is optimistic" \
        --end   "account for contingencies." \
        --body  "Consider adding a 20% buffer to each milestone."

  graphe comment list <file.md>
    List all comments with their IDs, anchors, and bodies.
    Example:
      graphe comment list post.md

  graphe comment edit <file.md> <id> [--start "..."] [--end "..."] [--body "..."]
    Update one or more fields of an existing comment. At least one flag is
    required. New anchors are validated against the current post source.
    Example:
      graphe comment edit post.md c-a1b2 --body "Revised: add a 15% buffer."

  graphe comment delete <file.md> <id>
    Remove a comment. The human typically does this after acting on feedback.
    Example:
      graphe comment delete post.md c-a1b2


TIPS FOR LLMs

- Write concise, actionable comments. One clear suggestion per comment is
  better than a long multi-point note.

- Quote the smallest substring that is unique. Do not paste whole paragraphs
  into --start or --end; a distinctive phrase of five to ten words is usually
  enough.

- If 'comment add' fails with "matches N places", extend the anchor by
  including a few words before or after the phrase until it is unique.

- The --body flag accepts markdown. Use **bold** for emphasis, backticks for
  inline code, and bullet lists for multi-point feedback.

- After adding all comments, run 'graphe comment list post.md' to confirm
  every comment was stored correctly before reporting back to the human.

- Do not edit the -review.json file directly. Always use the CLI so that
  anchor validation runs.
`
