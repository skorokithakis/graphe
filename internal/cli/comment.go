// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/skorokithakis/graphe/internal/post"
	"github.com/skorokithakis/graphe/internal/review"
	"github.com/spf13/cobra"
)

func init() {
	rootCommand.AddCommand(commentCommand)
	commentCommand.AddCommand(commentAddCommand)
	commentCommand.AddCommand(commentEditCommand)
	commentCommand.AddCommand(commentDeleteCommand)
	commentCommand.AddCommand(commentListCommand)

	commentAddCommand.Flags().String("start", "", "Unique substring marking the start of the anchor (required)")
	commentAddCommand.Flags().String("end", "", "Unique substring marking the end of the anchor (required)")
	commentAddCommand.Flags().String("body", "", "Comment text (required)")
	commentAddCommand.MarkFlagRequired("start")
	commentAddCommand.MarkFlagRequired("end")
	commentAddCommand.MarkFlagRequired("body")

	commentEditCommand.Flags().String("start", "", "Replace the start anchor with this substring")
	commentEditCommand.Flags().String("end", "", "Replace the end anchor with this substring")
	commentEditCommand.Flags().String("body", "", "Replace the comment body with this text")
}

var commentCommand = &cobra.Command{
	Use:   "comment",
	Short: "Manage review comments on a markdown post.",
	Long: `comment groups subcommands for adding, editing, deleting, and listing
review comments stored in the sidecar JSON file alongside a markdown post.

Each comment is anchored to a unique substring range in the post source.
The sidecar file is named <stem>-review.json and lives next to the .md file.`,
}

var commentAddCommand = &cobra.Command{
	Use:   "add <file.md>",
	Short: "Add a new review comment anchored to a text range.",
	Long: `add creates a new comment anchored to the range [--start, --end] in the
markdown source. Both anchors must appear exactly once in the document; if
either is ambiguous or missing, the command exits non-zero with an error.

On success, the new comment ID (e.g. "c-a1b2") is printed to stdout.

The comment is stored in a sidecar JSON file next to the markdown file:
  post.md -> post-review.json`,
	Example: `  graphe comment add post.md \
    --start "The quick brown fox" \
    --end "lazy dog" \
    --body "This sentence is a classic pangram."`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		filePath := args[0]

		startFlag, _ := cmd.Flags().GetString("start")
		endFlag, _ := cmd.Flags().GetString("end")
		bodyFlag, _ := cmd.Flags().GetString("body")

		loadedPost, err := post.Load(filePath)
		if err != nil {
			return fmt.Errorf("loading post: %w", err)
		}

		store, err := review.Load(filePath)
		if err != nil {
			return fmt.Errorf("loading review store: %w", err)
		}

		comment, err := store.Add(startFlag, endFlag, bodyFlag, loadedPost.Source)
		if err != nil {
			return err
		}

		fmt.Println(comment.ID)
		return nil
	},
}

var commentEditCommand = &cobra.Command{
	Use:   "edit <file.md> <id>",
	Short: "Edit an existing review comment.",
	Long: `edit updates one or more fields of the comment identified by <id>.
At least one of --start, --end, or --body must be provided.

When --start or --end is changed, the new anchor is validated against the
current post source. Changing only --body skips anchor revalidation.`,
	Example: `  # Update just the body:
  graphe comment edit post.md c-a1b2 --body "Revised note."

  # Move the anchor and update the body:
  graphe comment edit post.md c-a1b2 \
    --start "brown fox" \
    --body "Shorter anchor, same idea."`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		filePath := args[0]
		commentID := args[1]

		startFlag := flagStringOrNil(cmd, "start")
		endFlag := flagStringOrNil(cmd, "end")
		bodyFlag := flagStringOrNil(cmd, "body")

		if startFlag == nil && endFlag == nil && bodyFlag == nil {
			return fmt.Errorf("at least one of --start, --end, or --body must be provided")
		}

		loadedPost, err := post.Load(filePath)
		if err != nil {
			return fmt.Errorf("loading post: %w", err)
		}

		store, err := review.Load(filePath)
		if err != nil {
			return fmt.Errorf("loading review store: %w", err)
		}

		_, err = store.Edit(commentID, startFlag, endFlag, bodyFlag, loadedPost.Source)
		if err != nil {
			return err
		}

		return nil
	},
}

var commentDeleteCommand = &cobra.Command{
	Use:   "delete <file.md> <id>",
	Short: "Delete a review comment by ID.",
	Long: `delete removes the comment with the given ID from the sidecar JSON file.
The command exits non-zero if the ID does not exist.`,
	Example: `  graphe comment delete post.md c-a1b2`,
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		filePath := args[0]
		commentID := args[1]

		store, err := review.Load(filePath)
		if err != nil {
			return fmt.Errorf("loading review store: %w", err)
		}

		return store.Delete(commentID)
	},
}

var commentListCommand = &cobra.Command{
	Use:   "list <file.md>",
	Short: "List all review comments for a post.",
	Long: `list prints every comment in the sidecar JSON file, one block per comment.

Output format:

  c-a1b2  "the start text..." -> "...the end text"
    body line 1
    body line 2

Start and end previews are truncated to ~60 characters with "..." when longer.
The body is indented with two spaces. If there are no comments, nothing is printed.`,
	Example: `  graphe comment list post.md`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		filePath := args[0]

		store, err := review.Load(filePath)
		if err != nil {
			return fmt.Errorf("loading review store: %w", err)
		}

		comments := store.List()
		for _, comment := range comments {
			startPreview := truncate(comment.Start, 60)
			endPreview := truncate(comment.End, 60)
			fmt.Fprintf(os.Stdout, "%s  %q -> %q\n", comment.ID, startPreview, endPreview)
			for _, line := range strings.Split(comment.Body, "\n") {
				fmt.Fprintf(os.Stdout, "  %s\n", line)
			}
		}

		return nil
	},
}

// flagStringOrNil returns a pointer to the flag value if it was explicitly set
// by the user, or nil if the flag was not provided. This lets Edit distinguish
// "user passed --start ''" from "user did not pass --start at all".
func flagStringOrNil(cmd *cobra.Command, name string) *string {
	if !cmd.Flags().Changed(name) {
		return nil
	}
	value, _ := cmd.Flags().GetString(name)
	return &value
}

// truncate shortens text to at most maxLength runes, appending "..." when
// truncation occurs. The "..." is included within the maxLength budget so the
// result never exceeds maxLength runes.
func truncate(text string, maxLength int) string {
	runes := []rune(text)
	if len(runes) <= maxLength {
		return text
	}
	// Reserve three rune positions for the ellipsis.
	return string(runes[:maxLength-3]) + "..."
}
