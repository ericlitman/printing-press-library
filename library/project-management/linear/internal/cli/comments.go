package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func newCommentsCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "comments",
		Short: "Add or edit Linear comments with shell-safe markdown and media uploads",
		Long: `Add and edit Linear comments. Prefer --body-file or --body-stdin for
multi-line markdown, shell snippets, and agent-generated content. Media files
passed with --media are uploaded through Linear's fileUpload mutation and
inserted into the comment body as markdown image/link references.`,
		RunE: parentNoSubcommandRunE(flags),
	}
	cmd.AddCommand(newCommentsAddCmd(flags))
	cmd.AddCommand(newCommentsEditCmd(flags))
	return cmd
}

func newCommentsAddCmd(flags *rootFlags) *cobra.Command {
	var bodyInput markdownInputFlags
	var issueID, documentContentID, parentID, projectID, projectUpdateID, initiativeID, initiativeUpdateID, postID, quotedText string
	var media []string
	var publicMedia bool
	cmd := &cobra.Command{
		Use:     "add",
		Aliases: []string{"create"},
		Short:   "Add a Linear comment",
		Example: `  linear-pp-cli comments add --issue MOB-94 --body-file /tmp/comment.md
  linear-pp-cli comments add --issue MOB-94 --body-stdin < /tmp/comment.md
  linear-pp-cli comments add --issue MOB-94 --body-file /tmp/comment.md --media screenshot.png`,
		RunE: func(cmd *cobra.Command, args []string) error {
			body, bodySet, err := bodyInput.resolve(cmd, false)
			if err != nil {
				return err
			}
			if !bodySet && len(media) == 0 {
				return usageErr(fmt.Errorf("one of --body, --body-file, --body-stdin, or --media is required"))
			}
			input := map[string]any{}
			targetCount := 0
			for _, target := range []struct {
				name  string
				value string
			}{
				{"issueId", issueID},
				{"documentContentId", documentContentID},
				{"parentId", parentID},
				{"projectId", projectID},
				{"projectUpdateId", projectUpdateID},
				{"initiativeId", initiativeID},
				{"initiativeUpdateId", initiativeUpdateID},
				{"postId", postID},
			} {
				if target.value != "" {
					input[target.name] = target.value
					targetCount++
				}
			}
			if targetCount == 0 {
				return usageErr(fmt.Errorf("a comment target is required: pass --issue, --document-content, --parent, --project, --project-update, --initiative, --initiative-update, or --post"))
			}
			if targetCount > 1 {
				return usageErr(fmt.Errorf("choose exactly one comment target: --issue, --document-content, --parent, --project, --project-update, --initiative, --initiative-update, or --post"))
			}
			if quotedText != "" {
				input["quotedText"] = quotedText
			}

			if flags.dryRun {
				input["body"] = body
				out := map[string]any{
					"event":    "would_add_comment",
					"mutation": "commentCreate",
					"input":    input,
				}
				if len(media) > 0 {
					out["media"] = mediaDryRun(media, publicMedia)
					if !bodySet {
						out["note"] = "live run will upload media and append markdown links to the body"
					}
				}
				return writeCommandResult(cmd, flags, out)
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
			var assets []uploadedAsset
			if len(media) > 0 {
				assets, err = uploadMediaFiles(c, media, publicMedia)
				if err != nil {
					return err
				}
				body = appendMediaMarkdown(body, assets)
			}
			input["body"] = body

			const mutation = `mutation CreateComment($input: CommentCreateInput!) {
				commentCreate(input: $input) {
					success
					comment {
						id body url createdAt updatedAt
						issue { id identifier title url }
						parent { id url }
						documentContent { id document { id title url } }
					}
				}
			}`
			resp, err := c.Mutate(mutation, map[string]any{"input": input})
			if err != nil {
				return classifyAPIError(fmt.Errorf("commentCreate failed: %w", err), flags)
			}
			var parsed struct {
				CommentCreate struct {
					Success bool            `json:"success"`
					Comment json.RawMessage `json:"comment"`
				} `json:"commentCreate"`
			}
			if err := json.Unmarshal(resp, &parsed); err != nil {
				return fmt.Errorf("parsing commentCreate response: %w", err)
			}
			if !parsed.CommentCreate.Success {
				return fmt.Errorf("Linear reported commentCreate success=false")
			}
			return writeMutationPayload(cmd, flags, "comment_added", parsed.CommentCreate.Comment, assets)
		},
	}
	addBodyInputFlags(cmd, &bodyInput, "Comment body (markdown); prefer --body-file for multi-line content")
	cmd.Flags().StringVar(&issueID, "issue", "", "Issue UUID or identifier (e.g. MOB-94)")
	cmd.Flags().StringVar(&documentContentID, "document-content", "", "DocumentContent UUID for an inline document comment")
	cmd.Flags().StringVar(&parentID, "parent", "", "Parent comment UUID for a reply")
	cmd.Flags().StringVar(&projectID, "project", "", "Project UUID for a project-level discussion")
	cmd.Flags().StringVar(&projectUpdateID, "project-update", "", "Project update UUID")
	cmd.Flags().StringVar(&initiativeID, "initiative", "", "Initiative UUID")
	cmd.Flags().StringVar(&initiativeUpdateID, "initiative-update", "", "Initiative update UUID")
	cmd.Flags().StringVar(&postID, "post", "", "Post UUID")
	cmd.Flags().StringVar(&quotedText, "quoted-text", "", "Quoted text for inline comments")
	cmd.Flags().StringArrayVar(&media, "media", nil, "Upload a media/file path and append it to the comment body (repeatable)")
	cmd.Flags().BoolVar(&publicMedia, "media-public", false, "Make uploaded media publicly accessible instead of workspace-scoped")
	return cmd
}

func newCommentsEditCmd(flags *rootFlags) *cobra.Command {
	var bodyInput markdownInputFlags
	var media []string
	var publicMedia bool
	cmd := &cobra.Command{
		Use:     "edit <comment-id>",
		Aliases: []string{"update"},
		Short:   "Edit a Linear comment",
		Example: `  linear-pp-cli comments edit 550e8400-e29b-41d4-a716-446655440000 --body-file /tmp/comment.md
  linear-pp-cli comments edit 550e8400-e29b-41d4-a716-446655440000 --media screenshot.png`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, bodySet, err := bodyInput.resolve(cmd, false)
			if err != nil {
				return err
			}
			if !bodySet && len(media) == 0 {
				return usageErr(fmt.Errorf("one of --body, --body-file, --body-stdin, or --media is required"))
			}

			if flags.dryRun {
				input := map[string]any{}
				if bodySet {
					input["body"] = body
				}
				out := map[string]any{
					"event":      "would_edit_comment",
					"mutation":   "commentUpdate",
					"comment_id": args[0],
					"input":      input,
				}
				if len(media) > 0 {
					out["media"] = mediaDryRun(media, publicMedia)
					if !bodySet {
						out["note"] = "live run will fetch the existing comment body and append media markdown"
					}
				}
				return writeCommandResult(cmd, flags, out)
			}

			c, err := flags.newClient()
			if err != nil {
				return err
			}
			if len(media) > 0 && !bodySet {
				body, err = fetchCommentBody(c, args[0])
				if err != nil {
					return err
				}
			}
			var assets []uploadedAsset
			if len(media) > 0 {
				assets, err = uploadMediaFiles(c, media, publicMedia)
				if err != nil {
					return err
				}
				body = appendMediaMarkdown(body, assets)
			}
			input := map[string]any{"body": body}
			const mutation = `mutation UpdateComment($id: String!, $input: CommentUpdateInput!) {
				commentUpdate(id: $id, input: $input) {
					success
					comment {
						id body url createdAt updatedAt editedAt
						issue { id identifier title url }
						parent { id url }
						documentContent { id document { id title url } }
					}
				}
			}`
			resp, err := c.Mutate(mutation, map[string]any{"id": args[0], "input": input})
			if err != nil {
				return classifyAPIError(fmt.Errorf("commentUpdate failed: %w", err), flags)
			}
			var parsed struct {
				CommentUpdate struct {
					Success bool            `json:"success"`
					Comment json.RawMessage `json:"comment"`
				} `json:"commentUpdate"`
			}
			if err := json.Unmarshal(resp, &parsed); err != nil {
				return fmt.Errorf("parsing commentUpdate response: %w", err)
			}
			if !parsed.CommentUpdate.Success {
				return fmt.Errorf("Linear reported commentUpdate success=false")
			}
			return writeMutationPayload(cmd, flags, "comment_edited", parsed.CommentUpdate.Comment, assets)
		},
	}
	addBodyInputFlags(cmd, &bodyInput, "Replacement comment body (markdown); prefer --body-file for multi-line content")
	cmd.Flags().StringArrayVar(&media, "media", nil, "Upload a media/file path and append it to the comment body (repeatable)")
	cmd.Flags().BoolVar(&publicMedia, "media-public", false, "Make uploaded media publicly accessible instead of workspace-scoped")
	return cmd
}

func fetchCommentBody(c interface {
	QueryInto(string, map[string]any, any) error
}, id string) (string, error) {
	const query = `query GetCommentBody($id: String!) {
		comment(id: $id) { id body }
	}`
	var resp struct {
		Comment struct {
			ID   string `json:"id"`
			Body string `json:"body"`
		} `json:"comment"`
	}
	if err := c.QueryInto(query, map[string]any{"id": id}, &resp); err != nil {
		return "", fmt.Errorf("fetching existing comment %s: %w", id, err)
	}
	if resp.Comment.ID == "" {
		return "", notFoundErr(fmt.Errorf("comment %q not found", id))
	}
	return resp.Comment.Body, nil
}

func mediaDryRun(paths []string, public bool) []map[string]any {
	out := make([]map[string]any, 0, len(paths))
	for _, path := range paths {
		out = append(out, map[string]any{
			"path":        path,
			"will_upload": true,
			"make_public": public,
		})
	}
	return out
}

func writeMutationPayload(cmd *cobra.Command, flags *rootFlags, event string, payload json.RawMessage, assets []uploadedAsset) error {
	if flags.asJSON {
		var value any
		if err := json.Unmarshal(payload, &value); err != nil {
			return err
		}
		out := map[string]any{
			"event": event,
			"item":  value,
		}
		if len(assets) > 0 {
			out["media"] = assets
		}
		return writeCommandResult(cmd, flags, out)
	}
	var item struct {
		ID    string `json:"id"`
		URL   string `json:"url"`
		Issue *struct {
			Identifier string `json:"identifier"`
		} `json:"issue"`
	}
	if err := json.Unmarshal(payload, &item); err != nil {
		return fmt.Errorf("parsing %s response: %w", event, err)
	}
	out := cmd.OutOrStdout()
	if item.ID != "" {
		if item.Issue != nil && item.Issue.Identifier != "" {
			fmt.Fprintf(out, "%s %s on %s\n", event, item.ID, item.Issue.Identifier)
		} else {
			fmt.Fprintf(out, "%s %s\n", event, item.ID)
		}
	}
	if item.URL != "" {
		fmt.Fprintf(out, "  URL: %s\n", item.URL)
	}
	if len(assets) > 0 {
		fmt.Fprintf(out, "  Uploaded %d media file(s).\n", len(assets))
	}
	return nil
}

func writeCommandResult(cmd *cobra.Command, flags *rootFlags, v any) error {
	if flags.asJSON {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	return printJSONFiltered(cmd.OutOrStdout(), v, flags)
}
