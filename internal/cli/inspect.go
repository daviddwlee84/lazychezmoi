package cli

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/lazychezmoi/internal/search"
	"github.com/spf13/cobra"
)

func safeLine(text string) string {
	text = strings.TrimRight(text, "\r\n")
	return strings.Map(func(r rune) rune {
		if r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, ansi.Strip(text))
}

func (a *app) searchCommand() *cobra.Command {
	var scope string
	var regex, jsonMode bool
	c := &cobra.Command{Use: "search QUERY", Short: "Search source files or managed current contents with ripgrep", Args: args(cobra.ExactArgs(1)), RunE: func(cmd *cobra.Command, values []string) error {
		if scope != "source" && scope != "current" {
			return usage("scope must be source or current")
		}
		if values[0] == "" {
			return usage("search query must not be empty")
		}
		owner, cfg, err := a.service(cmd)
		if err != nil {
			return err
		}
		result, searchErr := search.New(search.Options{RG: cfg.Tools.RG}).Search(cmd.Context(), owner, search.Query{Scope: scope, Text: values[0], Regex: regex})
		if jsonMode {
			if err := jsonOut(cmd.OutOrStdout(), result); err != nil {
				return err
			}
		} else {
			for _, match := range result.Matches {
				fmt.Fprintf(cmd.OutOrStdout(), "%s:%d:%d: %s\n", safeLine(match.Relative), match.Line, match.Column, safeLine(match.Text))
			}
			if result.Truncated {
				fmt.Fprintln(cmd.ErrOrStderr(), "Results truncated at 1,000 matches; narrow the query.")
			}
			if result.Skipped > 0 {
				fmt.Fprintf(cmd.ErrOrStderr(), "Skipped %d unavailable or unsupported files.\n", result.Skipped)
			}
			for _, note := range result.Errors {
				fmt.Fprintln(cmd.ErrOrStderr(), safeLine(note))
			}
		}
		return searchErr
	}}
	c.Flags().StringVar(&scope, "scope", "source", "source repository or current managed files")
	c.Flags().BoolVar(&regex, "regex", false, "interpret QUERY as a ripgrep regular expression (default: literal smart case)")
	c.Flags().BoolVar(&jsonMode, "json", false, "emit matches and completeness information as JSON")
	c.RegisterFlagCompletionFunc("scope", enum("source", "current"))
	return c
}

func (a *app) hunksCommand() *cobra.Command {
	var jsonMode bool
	c := &cobra.Command{Use: "hunks TARGET", Short: "Inspect copyable Current/Source hunks and stable snapshot IDs", Args: args(cobra.ExactArgs(1)), RunE: func(cmd *cobra.Command, values []string) error {
		service, _, err := a.service(cmd)
		if err != nil {
			return err
		}
		entry, err := service.Locate(cmd.Context(), values[0])
		if err != nil {
			return err
		}
		snapshot, err := service.Hunks(cmd.Context(), entry)
		if err != nil {
			return err
		}
		if jsonMode {
			return jsonOut(cmd.OutOrStdout(), snapshot)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Current: %s\nSource:  %s\nSnapshot: %s\n", safeLine(entry.Target), safeLine(entry.Source), snapshot.ID)
		for _, note := range snapshot.Notes {
			fmt.Fprintln(cmd.OutOrStdout(), safeLine(note))
		}
		for _, hunk := range snapshot.Hunks {
			fmt.Fprintf(cmd.OutOrStdout(), "%s  %s\n", hunk.ID, safeLine(hunk.Header))
		}
		if len(snapshot.Hunks) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "No content hunks; metadata changes use native apply.")
		}
		return nil
	}}
	c.Flags().BoolVar(&jsonMode, "json", false, "emit snapshot and hunk metadata as JSON")
	return c
}

func (a *app) copyHunkCommand() *cobra.Command {
	var id, direction string
	var yes, jsonMode bool
	c := &cobra.Command{Use: "copy-hunk TARGET --id ID --direction DIRECTION --yes", Short: "Copy one previously inspected content hunk without native apply or state changes", Args: args(cobra.ExactArgs(1)), RunE: func(cmd *cobra.Command, values []string) error {
		if id == "" {
			return usage("--id is required; inspect `lazychezmoi hunks TARGET --json` first")
		}
		if direction != "source-to-current" && direction != "current-to-source" {
			return usage("--direction must be source-to-current or current-to-source")
		}
		if !yes {
			return usage("--yes is required to copy the inspected hunk; this changes the receiving file's contents")
		}
		service, _, err := a.service(cmd)
		if err != nil {
			return err
		}
		entry, err := service.Locate(cmd.Context(), values[0])
		if err != nil {
			return err
		}
		snapshot, err := service.Hunks(cmd.Context(), entry)
		if err != nil {
			return err
		}
		receipt, err := service.CopyHunk(cmd.Context(), snapshot, id, direction)
		if err != nil {
			return err
		}
		if jsonMode {
			return jsonOut(cmd.OutOrStdout(), receipt)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Copied hunk %s (%s) to %s\n", id, direction, safeLine(receipt.Path))
		return nil
	}}
	c.Flags().StringVar(&id, "id", "", "hunk ID from the reviewed snapshot; stale IDs are rejected")
	c.Flags().StringVar(&direction, "direction", "", "source-to-current or current-to-source")
	c.Flags().BoolVar(&yes, "yes", false, "confirm copying the exact hunk")
	c.Flags().BoolVar(&jsonMode, "json", false, "emit a receipt (content bytes are not included)")
	c.RegisterFlagCompletionFunc("direction", enum("source-to-current", "current-to-source"))
	return c
}
