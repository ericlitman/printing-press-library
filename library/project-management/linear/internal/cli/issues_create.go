package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/mvanhorn/printing-press-library/library/project-management/linear/internal/client"
	"github.com/mvanhorn/printing-press-library/library/project-management/linear/internal/store"

	"github.com/spf13/cobra"
)

// issueCreateMutation is the GraphQL mutation used to create a Linear issue.
// Shared by `issues create` and `import issues` via createIssueFromInput.
const issueCreateMutation = `mutation CreateIssue($input: IssueCreateInput!) {
	issueCreate(input: $input) {
		success
		issue {
			id identifier title url priority
			team { id key }
			state { id name type }
			assignee { id name displayName }
			project { id name }
		}
	}
}`

// createdIssue is the parsed result of an issueCreate mutation, carrying the
// fields both `issues create` and `import issues` render and write back to the
// local store.
type createdIssue struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
	URL        string `json:"url"`
	Priority   int    `json:"priority"`
	Team       struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	} `json:"team"`
	State struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"state"`
	Assignee *struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		DisplayName string `json:"displayName"`
	} `json:"assignee,omitempty"`
	Project *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"project,omitempty"`
}

// issueInputFromFlags assembles an IssueCreateInput map from the typed flag
// values. Pure (no I/O) so it is unit-testable and shared by the dry-run and
// live paths of `issues create`. Team resolution happens later in
// createIssueFromInput, which has the local store.
func issueInputFromFlags(title, team, desc, assignee, project, state string, priority int, labels []string) map[string]any {
	input := map[string]any{
		"title":  title,
		"teamId": team,
	}
	if desc != "" {
		input["description"] = desc
	}
	if priority > 0 {
		input["priority"] = priority
	}
	if assignee != "" {
		input["assigneeId"] = assignee
	}
	if project != "" {
		input["projectId"] = project
	}
	if state != "" {
		input["stateId"] = state
	}
	if len(labels) > 0 {
		input["labelIds"] = labels
	}
	return input
}

// createIssueFromInput runs the issueCreate mutation for a single
// IssueCreateInput map and, when db is non-nil, resolves a team key to a UUID
// before the call and records the new issue in the pp_created ledger with a
// local-store write-back afterwards. It is the shared create core for both
// `issues create` (flag-driven) and `import issues` (JSONL-driven).
//
// session is the already-resolved pp_created session tag; an empty/"current"
// value is normalized to the current run session before recording.
func createIssueFromInput(c *client.Client, db *store.Store, input map[string]any, session string) (*createdIssue, error) {
	// Resolve team key/name to UUID via the local store if possible. The
	// mutation requires a UUID; JSONL records and flag values may pass a key
	// like "ENG".
	if db != nil {
		if teamVal, ok := input["teamId"].(string); ok && teamVal != "" {
			if resolved, ok := resolveTeamID(db, teamVal); ok {
				input["teamId"] = resolved
			}
		}
	}

	resp, err := c.Mutate(issueCreateMutation, map[string]any{"input": input})
	if err != nil {
		return nil, fmt.Errorf("issueCreate failed: %w", err)
	}
	var parsed struct {
		IssueCreate struct {
			Success bool         `json:"success"`
			Issue   createdIssue `json:"issue"`
		} `json:"issueCreate"`
	}
	if err := json.Unmarshal(resp, &parsed); err != nil {
		return nil, fmt.Errorf("parsing issueCreate response: %w", err)
	}
	if !parsed.IssueCreate.Success {
		return nil, fmt.Errorf("Linear reported issueCreate success=false")
	}
	issue := parsed.IssueCreate.Issue

	if db != nil {
		sess := session
		if sess == "" || sess == "current" {
			sess = ppCurrentSession()
		}
		if recErr := db.RecordPPFixture(issue.ID, issue.Identifier, issue.Title, sess); recErr != nil {
			fmt.Fprintf(os.Stderr, "warning: pp_created ledger write failed: %v\n", recErr)
		}
		// Write-back to the local issues table so a subsequent
		// `issues list` from the local store sees the new ticket without
		// requiring a separate `sync --incremental`. The HTTP cache is
		// already invalidated by client.do on every non-GET success; this
		// closes the SQLite-store-side gap.
		wb := map[string]any{
			"id":         issue.ID,
			"identifier": issue.Identifier,
			"title":      issue.Title,
			"url":        issue.URL,
			"priority":   issue.Priority,
			"team": map[string]any{
				"id":  issue.Team.ID,
				"key": issue.Team.Key,
			},
			"teamId": issue.Team.ID,
			"state": map[string]any{
				"id":   issue.State.ID,
				"name": issue.State.Name,
				"type": issue.State.Type,
			},
			"createdAt": time.Now().UTC().Format(time.RFC3339),
			"updatedAt": time.Now().UTC().Format(time.RFC3339),
		}
		if issue.Assignee != nil {
			wb["assignee"] = map[string]any{
				"id":          issue.Assignee.ID,
				"name":        issue.Assignee.Name,
				"displayName": issue.Assignee.DisplayName,
			}
			wb["assigneeId"] = issue.Assignee.ID
		}
		if issue.Project != nil {
			wb["project"] = map[string]any{
				"id":   issue.Project.ID,
				"name": issue.Project.Name,
			}
			wb["projectId"] = issue.Project.ID
		}
		if newIssueJSON, mErr := json.Marshal(wb); mErr == nil {
			if upErr := db.UpsertIssue(issue.ID, issue.Identifier, issue.Title, newIssueJSON); upErr != nil {
				fmt.Fprintf(os.Stderr, "warning: local store write-back failed: %v\n", upErr)
			}
		}
	}

	return &issue, nil
}

// newIssuesCreateCmd is registered as a subcommand of "issues" via wireIssuesCreate
// in init(). Calls Linear's issueCreate mutation and records the resulting issue
// into the local pp_created ledger so pp-cleanup can find it later.
func newIssuesCreateCmd(flags *rootFlags) *cobra.Command {
	var titleFlag, teamFlag, descFlag, assigneeFlag, projectFlag, stateFlag string
	var priorityFlag int
	var labelsFlag []string
	var dbPath string
	var session string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new Linear issue and record it in the pp_created ledger",
		Long: `Create a Linear issue via the issueCreate mutation. The new issue's ID is
written to the local pp_created table along with a session tag, so pp-test
list shows it and pp-cleanup can archive it without touching pre-existing
tickets in the workspace.`,
		Example: `  # Quick test ticket in team ENG
  linear-pp-cli issues create --title "pp-test sanity" --team ENG

  # Dry-run (shows the GraphQL request without sending)
  linear-pp-cli issues create --title "x" --team ENG --dry-run

  # JSON output (agent-mode)
  linear-pp-cli issues create --title "x" --team ENG --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if titleFlag == "" {
				return fmt.Errorf("--title is required")
			}
			if teamFlag == "" {
				return fmt.Errorf("--team is required (team key like ENG or team UUID)")
			}
			// trust-mode strict requires the create call to include a session
			// or the explicit --pp-test marker so the resulting fixture is
			// always recoverable by pp-cleanup.
			if flags.trustMode == "strict" {
				sess := resolvePPSession(flags, session)
				if sess == "" {
					return fmt.Errorf("trust-mode=strict: pass --session <tag> (or set PP_SESSION env) so this fixture is recoverable by pp-cleanup")
				}
			}

			c, err := flags.newClient()
			if err != nil {
				return err
			}

			input := issueInputFromFlags(titleFlag, teamFlag, descFlag, assigneeFlag, projectFlag, stateFlag, priorityFlag, labelsFlag)

			if dbPath == "" {
				dbPath = defaultDBPath("linear-pp-cli")
			}

			if flags.dryRun {
				// Preview the input with team resolution applied when the
				// local store is available, matching the live path's behavior.
				if db, dbErr := store.Open(dbPath); dbErr == nil {
					defer db.Close()
					if resolved, ok := resolveTeamID(db, teamFlag); ok {
						input["teamId"] = resolved
					}
				}
				out := map[string]any{
					"event":    "would_create_issue",
					"mutation": "issueCreate",
					"input":    input,
				}
				if flags.asJSON {
					enc := json.NewEncoder(os.Stdout)
					enc.SetIndent("", "  ")
					return enc.Encode(out)
				}
				fmt.Printf("Would create issue: title=%q team=%s\n", titleFlag, input["teamId"])
				return nil
			}

			sess := resolvePPSession(flags, session)

			db, dbErr := store.Open(dbPath)
			if dbErr != nil {
				fmt.Fprintf(os.Stderr, "warning: cannot open ledger at %s: %v\n", dbPath, dbErr)
				db = nil
			} else {
				defer db.Close()
			}

			issue, err := createIssueFromInput(c, db, input, sess)
			if err != nil {
				return err
			}

			if sess == "" || sess == "current" {
				sess = ppCurrentSession()
			}

			if flags.asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]any{
					"event":      "issue_created",
					"identifier": issue.Identifier,
					"id":         issue.ID,
					"title":      issue.Title,
					"team":       issue.Team.Key,
					"state":      issue.State.Name,
					"url":        issue.URL,
					"session":    sess,
				})
			}
			fmt.Printf("Created %s — %s\n", issue.Identifier, issue.Title)
			fmt.Printf("  URL: %s\n", issue.URL)
			fmt.Printf("  Recorded in pp_created (session=%s) for safe pp-cleanup.\n", sess)
			return nil
		},
	}
	cmd.Flags().StringVar(&titleFlag, "title", "", "Issue title (required)")
	cmd.Flags().StringVar(&teamFlag, "team", "", "Team key (e.g. ENG) or team UUID (required)")
	cmd.Flags().StringVar(&descFlag, "description", "", "Issue description (markdown)")
	cmd.Flags().IntVar(&priorityFlag, "priority", 0, "Priority: 1=Urgent, 2=High, 3=Medium, 4=Low (0=None)")
	cmd.Flags().StringVar(&assigneeFlag, "assignee", "", "Assignee user UUID")
	cmd.Flags().StringVar(&projectFlag, "project", "", "Project UUID")
	cmd.Flags().StringVar(&stateFlag, "state", "", "Workflow state UUID")
	cmd.Flags().StringSliceVar(&labelsFlag, "label", nil, "Label UUIDs (repeatable)")
	cmd.Flags().StringVar(&dbPath, "db", "", "Database path (for team-key resolution and pp_created ledger)")
	cmd.Flags().StringVar(&session, "session", "", "Session tag (defaults to PP_SESSION env or current run timestamp)")
	return cmd
}

// resolveTeamID maps a team key (ENG, OPS) to a team UUID using the local
// teams cache. Returns ("", false) if the key isn't recognized — in that
// case the caller passes through the user's input unchanged (it may already
// be a UUID).
func resolveTeamID(db *store.Store, keyOrID string) (string, bool) {
	teams, err := db.ListTeams()
	if err != nil {
		return "", false
	}
	for _, raw := range teams {
		var t struct {
			ID  string `json:"id"`
			Key string `json:"key"`
		}
		if err := json.Unmarshal(raw, &t); err != nil {
			continue
		}
		if t.Key == keyOrID || t.ID == keyOrID {
			return t.ID, true
		}
	}
	return "", false
}
