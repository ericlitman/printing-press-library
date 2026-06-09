package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/mvanhorn/printing-press-library/library/project-management/linear/internal/client"
	"github.com/mvanhorn/printing-press-library/library/project-management/linear/internal/store"
	"github.com/spf13/cobra"
)

type issueMutationTarget struct {
	ID          string `json:"id"`
	Identifier  string `json:"identifier"`
	Title       string `json:"title"`
	Description string `json:"description"`
	URL         string `json:"url"`
}

func newIssuesEditCmd(flags *rootFlags) *cobra.Command {
	var titleFlag, assigneeFlag, projectFlag, stateFlag string
	var descInput markdownInputFlags
	var priorityFlag int
	var labelsFlag []string
	var media []string
	var publicMedia bool
	var dbPath string
	cmd := &cobra.Command{
		Use:     "edit <issue-id>",
		Aliases: []string{"update"},
		Short:   "Edit a Linear issue, including shell-safe markdown descriptions and media uploads",
		Example: `  linear-pp-cli issues edit MOB-94 --description-file /tmp/body.md
  linear-pp-cli issues edit MOB-94 --media screenshot.png
  linear-pp-cli issues update MOB-94 --title "Updated title" --priority 2`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolveMarkdown := descInput.resolve
			if flags.dryRun {
				resolveMarkdown = descInput.resolveDryRun
			}
			description, descSet, err := resolveMarkdown(cmd, false)
			if err != nil {
				return err
			}
			if titleFlag == "" && !descSet && priorityFlag == 0 && assigneeFlag == "" && projectFlag == "" && stateFlag == "" && len(labelsFlag) == 0 && len(media) == 0 {
				return usageErr(fmt.Errorf("nothing to update: pass --title, --description, --description-file, --description-stdin, --media, --priority, --assignee, --project, --state, or --label"))
			}
			if dbPath == "" {
				dbPath = defaultDBPath("linear-pp-cli")
			}

			input := map[string]any{}
			addOptionalString(input, "title", titleFlag)
			if descSet {
				input["description"] = description
			}
			if priorityFlag > 0 {
				input["priority"] = priorityFlag
			}
			addOptionalString(input, "assigneeId", assigneeFlag)
			addOptionalString(input, "projectId", projectFlag)
			addOptionalString(input, "stateId", stateFlag)
			if len(labelsFlag) > 0 {
				input["labelIds"] = labelsFlag
			}

			if flags.dryRun {
				out := map[string]any{
					"event":    "would_edit_issue",
					"mutation": "issueUpdate",
					"issue_id": args[0],
					"input":    input,
				}
				if len(media) > 0 {
					out["media"] = mediaDryRun(media, publicMedia)
					if !descSet {
						out["note"] = "live run will fetch the existing issue description and append media markdown"
					}
				}
				if !flags.asJSON {
					fmt.Fprintf(cmd.OutOrStdout(), "Would edit issue %s\n", args[0])
					if len(media) > 0 {
						fmt.Fprintf(cmd.OutOrStdout(), "Would upload %d media file(s) and append markdown links to the description.\n", len(media))
					}
					return nil
				}
				return writeCommandResult(cmd, flags, out)
			}

			c, err := flags.newClient()
			if err != nil {
				return err
			}
			target, err := fetchIssueMutationTarget(c, args[0])
			if err != nil {
				return err
			}
			if err := requirePPCreatedIfStrict(flags, dbPath, target.ID); err != nil {
				return err
			}
			if len(media) > 0 && !descSet {
				description = target.Description
			}
			var assets []uploadedAsset
			if len(media) > 0 {
				assets, err = uploadMediaFiles(c, media, publicMedia)
				if err != nil {
					return err
				}
				description = appendMediaMarkdown(description, assets)
				input["description"] = description
			}

			const mutation = `mutation UpdateIssue($id: String!, $input: IssueUpdateInput!) {
				issueUpdate(id: $id, input: $input) {
					success
					issue {
						id identifier title description url priority estimate dueDate updatedAt createdAt
						team { id key name }
						state { id name type }
						assignee { id name displayName email }
						project { id name }
						cycle { id name number }
						labels { nodes { id name color } }
						parent { id identifier title }
					}
				}
			}`
			resp, err := c.Mutate(mutation, map[string]any{"id": target.ID, "input": input})
			if err != nil {
				return classifyAPIError(mutationErrorAfterMediaUpload("issueUpdate", err, assets), flags)
			}
			var parsed struct {
				IssueUpdate struct {
					Success bool            `json:"success"`
					Issue   json.RawMessage `json:"issue"`
				} `json:"issueUpdate"`
			}
			if err := json.Unmarshal(resp, &parsed); err != nil {
				return fmt.Errorf("parsing issueUpdate response: %w", err)
			}
			if !parsed.IssueUpdate.Success {
				return mutationErrorAfterMediaUpload("issueUpdate", fmt.Errorf("Linear reported success=false"), assets)
			}
			writeBackIssue(cmd.ErrOrStderr(), dbPath, parsed.IssueUpdate.Issue)
			return writeIssueMutationPayload(cmd, flags, "issue_edited", parsed.IssueUpdate.Issue, assets)
		},
	}
	cmd.Flags().StringVar(&titleFlag, "title", "", "Replacement issue title")
	addDescriptionInputFlags(cmd, &descInput, "Replacement issue description (markdown); prefer --description-file for multi-line content")
	cmd.Flags().IntVar(&priorityFlag, "priority", 0, "Priority: 1=Urgent, 2=High, 3=Medium, 4=Low")
	cmd.Flags().StringVar(&assigneeFlag, "assignee", "", "Assignee user UUID")
	cmd.Flags().StringVar(&projectFlag, "project", "", "Project UUID")
	cmd.Flags().StringVar(&stateFlag, "state", "", "Workflow state UUID")
	cmd.Flags().StringSliceVar(&labelsFlag, "label", nil, "Replacement label UUIDs (repeatable)")
	cmd.Flags().StringArrayVar(&media, "media", nil, "Upload a media/file path and append it to the issue description (repeatable)")
	cmd.Flags().BoolVar(&publicMedia, "media-public", false, "Make uploaded media publicly accessible instead of workspace-scoped")
	cmd.Flags().StringVar(&dbPath, "db", "", "Database path (for trust-mode and local write-back)")
	return cmd
}

func fetchIssueMutationTarget(c *client.Client, id string) (issueMutationTarget, error) {
	var raw json.RawMessage
	var err error
	if isIssueUUID(id) {
		raw, err = fetchIssueByIDForMutation(c, id)
	} else if _, _, ok := parseIssueIdentifier(id); ok {
		raw, err = fetchIssueLiveForMutation(c, id)
	} else {
		raw, err = fetchIssueByIDForMutation(c, id)
	}
	if err != nil {
		return issueMutationTarget{}, fmt.Errorf("fetching issue %s: %w", id, err)
	}
	var target issueMutationTarget
	if err := json.Unmarshal(raw, &target); err != nil {
		return issueMutationTarget{}, fmt.Errorf("parsing issue %s: %w", id, err)
	}
	if target.ID == "" {
		return issueMutationTarget{}, notFoundErr(fmt.Errorf("issue %q not found", id))
	}
	return target, nil
}

func fetchIssueByID(c *client.Client, id string) (json.RawMessage, error) {
	const query = `query GetIssueByID($id: String!) {
		issue(id: $id) {
			id identifier title description priority estimate dueDate url updatedAt createdAt
			state { name type }
			team { id key name }
			project { id name }
			assignee { id name displayName email }
		}
	}`
	var resp struct {
		Issue json.RawMessage `json:"issue"`
	}
	if err := c.QueryInto(query, map[string]any{"id": id}, &resp); err != nil {
		return nil, err
	}
	if len(resp.Issue) == 0 || string(resp.Issue) == "null" {
		return nil, notFoundErr(fmt.Errorf("issue %q not found", id))
	}
	return resp.Issue, nil
}

func fetchIssueByIDForMutation(c *client.Client, id string) (json.RawMessage, error) {
	const query = `query GetIssueForMutation($id: String!) {
		issue(id: $id) { id identifier title description url }
	}`
	var resp struct {
		Issue json.RawMessage `json:"issue"`
	}
	if err := c.QueryInto(query, map[string]any{"id": id}, &resp); err != nil {
		return nil, err
	}
	return resp.Issue, nil
}

func fetchIssueLiveForMutation(c *client.Client, identifier string) (json.RawMessage, error) {
	teamKey, number, ok := parseIssueIdentifier(identifier)
	if !ok {
		return nil, fmt.Errorf("invalid issue identifier %q (expected TEAM-NUMBER, e.g. ESP-1155)", identifier)
	}
	query := `query($teamKey: String!, $number: Float!) {
		issues(filter: { team: { key: { eq: $teamKey } }, number: { eq: $number } }, first: 1) {
			nodes { id identifier title description url }
		}
	}`
	var resp struct {
		Issues struct {
			Nodes []json.RawMessage `json:"nodes"`
		} `json:"issues"`
	}
	if err := c.QueryInto(query, map[string]any{"teamKey": teamKey, "number": number}, &resp); err != nil {
		return nil, err
	}
	if len(resp.Issues.Nodes) == 0 {
		return nil, notFoundErr(fmt.Errorf("issue %q not found", identifier))
	}
	return resp.Issues.Nodes[0], nil
}

func requireIssueScopedMutationIfStrict(flags *rootFlags, dbPath, issueID, surface string) error {
	if flags == nil || flags.trustMode != "strict" {
		return nil
	}
	if issueID == "" {
		return fmt.Errorf("trust-mode=strict: refusing to mutate %s because it is not scoped to a pp_created issue", surface)
	}
	return requirePPCreatedIfStrict(flags, dbPath, issueID)
}

func resolveIssueIDForMutation(c *client.Client, id string) (string, error) {
	if id == "" || isIssueUUID(id) {
		return id, nil
	}
	if _, _, ok := parseIssueIdentifier(id); !ok {
		return id, nil
	}
	target, err := fetchIssueMutationTarget(c, id)
	if err != nil {
		return "", err
	}
	return target.ID, nil
}

func requirePPCreatedIfStrict(flags *rootFlags, dbPath, issueID string) error {
	if flags == nil || flags.trustMode != "strict" {
		return nil
	}
	if _, err := os.Stat(dbPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("trust-mode=strict: no local store found at %s; run 'linear-pp-cli sync' first", dbPath)
		}
		return fmt.Errorf("trust-mode=strict: checking local store %s: %w", dbPath, err)
	}
	db, err := store.Open(dbPath)
	if err != nil {
		return fmt.Errorf("trust-mode=strict: opening pp_created ledger: %w", err)
	}
	defer db.Close()
	ok, err := db.IsPPCreated(issueID)
	if err != nil {
		return fmt.Errorf("trust-mode=strict: checking pp_created ledger: %w", err)
	}
	if !ok {
		return fmt.Errorf("trust-mode=strict: refusing to mutate issue %s because it is not in the local pp_created ledger", issueID)
	}
	return nil
}

func writeBackIssue(errOut io.Writer, dbPath string, raw json.RawMessage) {
	var issue struct {
		ID         string `json:"id"`
		Identifier string `json:"identifier"`
		Title      string `json:"title"`
		UpdatedAt  string `json:"updatedAt"`
		CreatedAt  string `json:"createdAt"`
	}
	if err := json.Unmarshal(raw, &issue); err != nil {
		fmt.Fprintf(errOut, "warning: local store write-back skipped: parsing issue response failed: %v\n", err)
		return
	}
	if issue.ID == "" {
		fmt.Fprintln(errOut, "warning: local store write-back skipped: issue response missing id")
		return
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		fmt.Fprintf(errOut, "warning: local store write-back timestamp normalization skipped: %v\n", err)
		obj = nil
	}
	db, err := store.Open(dbPath)
	if err != nil {
		fmt.Fprintf(errOut, "warning: local store write-back failed: %v\n", err)
		return
	}
	defer db.Close()
	if obj != nil {
		if existing, err := db.GetByID("issues", issue.ID); err == nil && len(existing) > 0 {
			var merged map[string]any
			if err := json.Unmarshal(existing, &merged); err == nil {
				obj = mergeIssuePayload(merged, obj)
			} else {
				fmt.Fprintf(errOut, "warning: local store write-back merge skipped: %v\n", err)
			}
		} else if err != nil {
			fmt.Fprintf(errOut, "warning: local store write-back merge skipped: %v\n", err)
		}
		obj["updatedAt"] = firstNonEmpty(issue.UpdatedAt, time.Now().UTC().Format(time.RFC3339))
		if issue.CreatedAt == "" {
			if createdAt, ok := obj["createdAt"]; !ok || createdAt == nil || fmt.Sprint(createdAt) == "" {
				obj["createdAt"] = time.Now().UTC().Format(time.RFC3339)
			}
		}
		if data, err := json.Marshal(obj); err == nil {
			raw = data
		} else {
			fmt.Fprintf(errOut, "warning: local store write-back timestamp normalization failed: %v\n", err)
		}
	}
	if upErr := db.UpsertIssue(issue.ID, issue.Identifier, issue.Title, raw); upErr != nil {
		fmt.Fprintf(errOut, "warning: local store write-back failed: %v\n", upErr)
	}
}

func mergeIssuePayload(existing, update map[string]any) map[string]any {
	for key, value := range update {
		updateMap, updateOK := value.(map[string]any)
		existingMap, existingOK := existing[key].(map[string]any)
		if updateOK && existingOK {
			existing[key] = mergeIssuePayload(existingMap, updateMap)
			continue
		}
		existing[key] = value
	}
	return existing
}

func writeIssueMutationPayload(cmd *cobra.Command, flags *rootFlags, event string, payload json.RawMessage, assets []uploadedAsset) error {
	if flags.asJSON {
		var value any
		if err := json.Unmarshal(payload, &value); err != nil {
			return err
		}
		out := map[string]any{"event": event, "issue": value}
		if len(assets) > 0 {
			out["media"] = assets
		}
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	var item struct {
		Identifier string `json:"identifier"`
		Title      string `json:"title"`
		URL        string `json:"url"`
	}
	if err := json.Unmarshal(payload, &item); err != nil {
		return fmt.Errorf("parsing %s response: %w", event, err)
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "%s %s — %s\n", event, item.Identifier, item.Title)
	if item.URL != "" {
		fmt.Fprintf(out, "  URL: %s\n", item.URL)
	}
	if len(assets) > 0 {
		fmt.Fprintf(out, "  Uploaded %d media file(s).\n", len(assets))
	}
	return nil
}
