package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func newDocumentsCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "documents [id]",
		Short: "View, create, or edit Linear documents with shell-safe markdown",
		Long: `View, create, and edit Linear documents. Prefer --content-file or
--content-stdin for multi-line markdown and agent-generated content.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			return runDocumentsGet(cmd, flags, args[0])
		},
	}
	cmd.AddCommand(newDocumentsCreateCmd(flags))
	cmd.AddCommand(newDocumentsEditCmd(flags))
	return cmd
}

func newDocumentsCreateCmd(flags *rootFlags) *cobra.Command {
	var title, icon, color, issueID, projectID, teamID, initiativeID, cycleID, releaseID string
	var contentInput markdownInputFlags
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a Linear document",
		Example: `  linear-pp-cli documents create --title "Session report" --issue MOB-94 --content-file /tmp/report.md
  linear-pp-cli documents create --title "Runbook" --team 550e8400-e29b-41d4-a716-446655440000 --content-stdin < /tmp/runbook.md`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if title == "" {
				return usageErr(fmt.Errorf("--title is required"))
			}
			content, contentSet, err := contentInput.resolve(cmd, false)
			if err != nil {
				return err
			}
			input := map[string]any{"title": title}
			if contentSet {
				input["content"] = content
			}
			addOptionalString(input, "icon", icon)
			addOptionalString(input, "color", color)
			addOptionalString(input, "issueId", issueID)
			addOptionalString(input, "projectId", projectID)
			addOptionalString(input, "teamId", teamID)
			addOptionalString(input, "initiativeId", initiativeID)
			addOptionalString(input, "cycleId", cycleID)
			addOptionalString(input, "releaseId", releaseID)

			if flags.dryRun {
				return writeCommandResult(cmd, flags, map[string]any{
					"event":    "would_create_document",
					"mutation": "documentCreate",
					"input":    input,
				})
			}

			c, err := flags.newClient()
			if err != nil {
				return err
			}
			if issueID != "" {
				resolvedIssueID, err := resolveIssueIDForMutation(c, issueID)
				if err != nil {
					return err
				}
				input["issueId"] = resolvedIssueID
			}
			const mutation = `mutation CreateDocument($input: DocumentCreateInput!) {
				documentCreate(input: $input) {
					success
					document {
						id title slugId url createdAt updatedAt
						content documentContentId
						issue { id identifier title url }
						project { id name url }
						team { id key name }
					}
				}
			}`
			resp, err := c.Mutate(mutation, map[string]any{"input": input})
			if err != nil {
				return classifyAPIError(fmt.Errorf("documentCreate failed: %w", err), flags)
			}
			var parsed struct {
				DocumentCreate struct {
					Success  bool            `json:"success"`
					Document json.RawMessage `json:"document"`
				} `json:"documentCreate"`
			}
			if err := json.Unmarshal(resp, &parsed); err != nil {
				return fmt.Errorf("parsing documentCreate response: %w", err)
			}
			if !parsed.DocumentCreate.Success {
				return fmt.Errorf("Linear reported documentCreate success=false")
			}
			return writeDocumentMutationPayload(cmd, flags, "document_created", parsed.DocumentCreate.Document)
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "Document title (required)")
	addContentInputFlags(cmd, &contentInput, "Document content (markdown); prefer --content-file for multi-line content")
	cmd.Flags().StringVar(&icon, "icon", "", "Document icon")
	cmd.Flags().StringVar(&color, "color", "", "Document icon color")
	cmd.Flags().StringVar(&issueID, "issue", "", "Related issue UUID or identifier (e.g. MOB-94)")
	cmd.Flags().StringVar(&projectID, "project", "", "Related project UUID")
	cmd.Flags().StringVar(&teamID, "team", "", "Related team UUID")
	cmd.Flags().StringVar(&initiativeID, "initiative", "", "Related initiative UUID")
	cmd.Flags().StringVar(&cycleID, "cycle", "", "Related cycle UUID")
	cmd.Flags().StringVar(&releaseID, "release", "", "Related release UUID")
	return cmd
}

func newDocumentsEditCmd(flags *rootFlags) *cobra.Command {
	var title, icon, color string
	var contentInput markdownInputFlags
	cmd := &cobra.Command{
		Use:     "edit <document-id-or-slug>",
		Aliases: []string{"update"},
		Short:   "Edit a Linear document",
		Example: `  linear-pp-cli documents edit 550e8400-e29b-41d4-a716-446655440000 --content-file /tmp/report.md
  linear-pp-cli documents edit MOB-94-session-report --title "Updated title"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			content, contentSet, err := contentInput.resolve(cmd, false)
			if err != nil {
				return err
			}
			input := map[string]any{}
			if title != "" {
				input["title"] = title
			}
			if contentSet {
				input["content"] = content
			}
			addOptionalString(input, "icon", icon)
			addOptionalString(input, "color", color)
			if len(input) == 0 {
				return usageErr(fmt.Errorf("nothing to update: pass --title, --content, --content-file, --content-stdin, --icon, or --color"))
			}

			if flags.dryRun {
				return writeCommandResult(cmd, flags, map[string]any{
					"event":       "would_edit_document",
					"mutation":    "documentUpdate",
					"document_id": args[0],
					"input":       input,
				})
			}

			c, err := flags.newClient()
			if err != nil {
				return err
			}
			const mutation = `mutation UpdateDocument($id: String!, $input: DocumentUpdateInput!) {
				documentUpdate(id: $id, input: $input) {
					success
					document {
						id title slugId url createdAt updatedAt
						content documentContentId
						issue { id identifier title url }
						project { id name url }
						team { id key name }
					}
				}
			}`
			resp, err := c.Mutate(mutation, map[string]any{"id": args[0], "input": input})
			if err != nil {
				return classifyAPIError(fmt.Errorf("documentUpdate failed: %w", err), flags)
			}
			var parsed struct {
				DocumentUpdate struct {
					Success  bool            `json:"success"`
					Document json.RawMessage `json:"document"`
				} `json:"documentUpdate"`
			}
			if err := json.Unmarshal(resp, &parsed); err != nil {
				return fmt.Errorf("parsing documentUpdate response: %w", err)
			}
			if !parsed.DocumentUpdate.Success {
				return fmt.Errorf("Linear reported documentUpdate success=false")
			}
			return writeDocumentMutationPayload(cmd, flags, "document_edited", parsed.DocumentUpdate.Document)
		},
	}
	addContentInputFlags(cmd, &contentInput, "Replacement document content (markdown); prefer --content-file for multi-line content")
	cmd.Flags().StringVar(&title, "title", "", "Replacement document title")
	cmd.Flags().StringVar(&icon, "icon", "", "Replacement document icon")
	cmd.Flags().StringVar(&color, "color", "", "Replacement document icon color")
	return cmd
}

func runDocumentsGet(cmd *cobra.Command, flags *rootFlags, id string) error {
	c, err := flags.newClient()
	if err != nil {
		return err
	}
	const query = `query GetDocument($id: String!) {
		document(id: $id) {
			id title slugId url createdAt updatedAt
			content documentContentId
			issue { id identifier title url }
			project { id name url }
			team { id key name }
		}
	}`
	var resp struct {
		Document json.RawMessage `json:"document"`
	}
	if err := c.QueryInto(query, map[string]any{"id": id}, &resp); err != nil {
		return classifyAPIError(fmt.Errorf("document fetch failed: %w", err), flags)
	}
	if flags.asJSON || (!isTerminal(cmd.OutOrStdout()) && !flags.csv && !flags.quiet && !flags.plain) {
		filtered := resp.Document
		if flags.selectFields != "" {
			filtered = filterFields(filtered, flags.selectFields)
		} else if flags.compact {
			filtered = compactFields(filtered)
		}
		return printOutput(cmd.OutOrStdout(), filtered, true)
	}
	return printOutputWithFlags(cmd.OutOrStdout(), resp.Document, flags)
}

func writeDocumentMutationPayload(cmd *cobra.Command, flags *rootFlags, event string, payload json.RawMessage) error {
	if flags.asJSON {
		var value any
		if err := json.Unmarshal(payload, &value); err != nil {
			return err
		}
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{"event": event, "document": value})
	}
	var item struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		URL   string `json:"url"`
	}
	if err := json.Unmarshal(payload, &item); err != nil {
		return fmt.Errorf("parsing %s response: %w", event, err)
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "%s %s — %s\n", event, item.ID, item.Title)
	if item.URL != "" {
		fmt.Fprintf(out, "  URL: %s\n", item.URL)
	}
	return nil
}

func addOptionalString(input map[string]any, key, value string) {
	if value != "" {
		input[key] = value
	}
}
