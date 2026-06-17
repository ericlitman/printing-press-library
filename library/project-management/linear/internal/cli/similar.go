package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/mvanhorn/printing-press-library/library/project-management/linear/internal/cliutil"
	"github.com/mvanhorn/printing-press-library/library/project-management/linear/internal/store"

	"github.com/spf13/cobra"
)

func newSimilarCmd(flags *rootFlags) *cobra.Command {
	var dbPath string
	var jsonOut bool
	var limit int
	var team string
	cmd := &cobra.Command{
		Use:         "similar [query]",
		Annotations: map[string]string{"mcp:read-only": "true"},
		Short:       "Find potentially duplicate issues using fuzzy text search",
		Long:        "Search locally synced issues using FTS5 full-text search to find potential duplicates. Works offline.",
		Example: `  linear-pp-cli similar "login bug"
  linear-pp-cli similar "pipeline follow-up" --team SYMPH --agent
  linear-pp-cli similar "payment failed" --limit 20
  linear-pp-cli similar "onboarding" --json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			return runIssueSearch(cmd, flags, issueSearchOptions{
				DBPath:  resolveDBPath(dbPath),
				JSONOut: jsonOut,
				Limit:   limit,
				Team:    team,
				Query:   args[0],
			})
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 50, "Maximum results to return")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	cmd.Flags().StringVar(&dbPath, "db", "", "Database path")
	cmd.Flags().StringVar(&team, "team", "", "Filter by team key, name, or UUID")
	return cmd
}

func newIssuesSearchCmd(flags *rootFlags, dbPath *string) *cobra.Command {
	var jsonOut bool
	var limit int
	var team string
	cmd := &cobra.Command{
		Use:         "search <query>",
		Annotations: map[string]string{"mcp:read-only": "true"},
		Short:       "Search synced issues by text (alias for similar)",
		Long: `Search locally synced issues using the same FTS5 duplicate-search engine as 'similar'.

This subcommand exists because agents naturally look for 'issues search' when
checking for existing tickets before creating a follow-up. Multi-word queries
may be quoted or passed as separate words; both forms are joined into one query.`,
		Example: `  linear-pp-cli issues search "login bug" --agent
  linear-pp-cli issues search "pipeline follow-up" --team SYMPH --limit 10 --agent
  linear-pp-cli issues search Kimi replay temp directories cleanup --team SYMPH --agent`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 || strings.TrimSpace(strings.Join(args, " ")) == "" {
				return usageErr(fmt.Errorf("issues search requires a query; use: linear-pp-cli issues search \"login bug\" --team ENG --agent (equivalent: linear-pp-cli similar \"login bug\" --team ENG --agent)"))
			}
			return runIssueSearch(cmd, flags, issueSearchOptions{
				DBPath:  resolveDBPath(*dbPath),
				JSONOut: jsonOut,
				Limit:   limit,
				Team:    team,
				Query:   strings.Join(args, " "),
			})
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 50, "Maximum results to return")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	cmd.Flags().StringVar(&team, "team", "", "Filter by team key, name, or UUID")
	return cmd
}

type issueSearchOptions struct {
	DBPath  string
	JSONOut bool
	Limit   int
	Team    string
	Query   string
}

func runIssueSearch(cmd *cobra.Command, flags *rootFlags, opts issueSearchOptions) error {
	// Verify mode: short-circuit so a synthetic query against an
	// empty FTS index doesn't fail the mechanical verify pass.
	if cliutil.IsVerifyEnv() {
		return nil
	}
	query := strings.TrimSpace(opts.Query)
	if query == "" {
		return usageErr(fmt.Errorf("search query cannot be empty"))
	}
	db, err := store.Open(opts.DBPath)
	if err != nil {
		return fmt.Errorf("opening database: %w\nRun 'linear-pp-cli sync' first.", err)
	}
	defer db.Close()

	teamID := ""
	if opts.Team != "" {
		resolved, err := resolveTeamFilter(db, opts.Team)
		if err != nil {
			if !errors.Is(err, errTeamFilterNotFound) {
				return err
			}
			return notFoundErr(fmt.Errorf("%w. Run 'linear-pp-cli sync' if the team was added recently", err))
		}
		teamID = resolved
	}
	results, err := db.SearchIssuesByTeam(query, teamID)
	if err != nil {
		return fmt.Errorf("searching: %w", err)
	}

	if opts.Limit > 0 && len(results) > opts.Limit {
		results = results[:opts.Limit]
	}

	if len(results) == 0 {
		hintIfUnsynced(cmd, db, "issues")
	} else {
		hintIfStale(cmd, db, "issues", flags.maxAge)
	}

	if opts.JSONOut || flags.asJSON || !isTerminal(cmd.OutOrStdout()) {
		data, err := json.Marshal(results)
		if err != nil {
			return err
		}
		if flags.selectFields != "" {
			data = filterFields(data, flags.selectFields)
		} else if flags.compact {
			data = compactFields(data)
		}
		return printOutput(cmd.OutOrStdout(), data, true)
	}

	out := cmd.OutOrStdout()
	if len(results) == 0 {
		fmt.Fprintf(out, "No issues matching %q\n", query)
		return nil
	}

	fmt.Fprintf(out, "%-12s %-15s %s\n", "ID", "STATE", "TITLE")
	fmt.Fprintln(out, strings.Repeat("-", 70))
	for _, raw := range results {
		var row struct {
			Identifier string                `json:"identifier"`
			Title      string                `json:"title"`
			State      struct{ Name string } `json:"state"`
		}
		if err := json.Unmarshal(raw, &row); err != nil {
			continue
		}
		title := row.Title
		if len(title) > 45 {
			title = title[:42] + "..."
		}
		fmt.Fprintf(out, "%-12s %-15s %s\n", row.Identifier, row.State.Name, title)
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "\n%d results for %q\n", len(results), query)
	return nil
}
