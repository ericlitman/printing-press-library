package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mvanhorn/printing-press-library/library/project-management/linear/internal/client"
	"github.com/mvanhorn/printing-press-library/library/project-management/linear/internal/store"

	"github.com/spf13/cobra"
)

func TestRenderIssueSelectDescriptionBeatsAgentCompact(t *testing.T) {
	t.Parallel()
	data := json.RawMessage(`{
		"identifier":"SYMPH-310",
		"title":"Follow-up",
		"description":"literal body with $(expansion) and ` + "`backticks`" + `",
		"state":{"name":"Backlog"}
	}`)
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	flags := &rootFlags{asJSON: true, compact: true, selectFields: "identifier,description"}
	if err := renderIssue(cmd, flags, data, DataProvenance{Source: "live", ResourceType: "issues"}); err != nil {
		t.Fatalf("renderIssue: %v", err)
	}
	var got struct {
		Results struct {
			Identifier  string `json:"identifier"`
			Description string `json:"description"`
			Title       string `json:"title"`
		} `json:"results"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out.String())
	}
	if got.Results.Description == "" {
		t.Fatalf("description was stripped under --agent + --select: %s", out.String())
	}
	if got.Results.Title != "" {
		t.Fatalf("unselected title leaked into output: %s", out.String())
	}
}

func TestCommentsAddReadsBodyFileLiterally(t *testing.T) {
	body := "Source body with $(danger), ${vars}, `backticks`, and GraphQL $input: String!\n"
	bodyPath := filepath.Join(t.TempDir(), "comment.md")
	if err := os.WriteFile(bodyPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var seenBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req client.GraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		switch {
		case strings.Contains(req.Query, "issues(filter"):
			fmt.Fprint(w, `{"data":{"issues":{"nodes":[{"id":"issue-uuid"}]}}}`)
		case strings.Contains(req.Query, "commentCreate"):
			input, _ := req.Variables["input"].(map[string]any)
			seenBody, _ = input["body"].(string)
			fmt.Fprint(w, `{"data":{"commentCreate":{"success":true,"comment":{"id":"comment-1","body":"ok","createdAt":"2026-06-09T00:00:00Z","updatedAt":"2026-06-09T00:00:00Z","user":{"id":"user-1","name":"eric","displayName":"eric","email":"e@example.com"},"issue":{"id":"issue-uuid","identifier":"MOB-99","title":"Issue"}}}}}`)
		default:
			t.Errorf("unexpected query: %s", req.Query)
			http.Error(w, "unexpected query", http.StatusBadRequest)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("LINEAR_BASE_URL", srv.URL)
	t.Setenv("LINEAR_API_KEY", "test-token")

	out, err := executeRootForTest("comments", "add", "--issue", "MOB-99", "--body-file", bodyPath, "--agent", "--data-source", "live")
	if err != nil {
		t.Fatalf("comments add failed: %v\n%s", err, out)
	}
	if seenBody != body {
		t.Fatalf("body sent to GraphQL = %q, want literal %q", seenBody, body)
	}
}

func TestCommentsAddReadsBodyStdinLiterally(t *testing.T) {
	body := "stdin body with $(danger), ${vars}, `backticks`, and GraphQL $input: String!\n"
	var seenBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req client.GraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		switch {
		case strings.Contains(req.Query, "issues(filter"):
			fmt.Fprint(w, `{"data":{"issues":{"nodes":[{"id":"issue-uuid"}]}}}`)
		case strings.Contains(req.Query, "commentCreate"):
			input, _ := req.Variables["input"].(map[string]any)
			seenBody, _ = input["body"].(string)
			fmt.Fprint(w, `{"data":{"commentCreate":{"success":true,"comment":{"id":"comment-1","body":"ok","createdAt":"2026-06-09T00:00:00Z","updatedAt":"2026-06-09T00:00:00Z","user":{"id":"user-1","name":"eric","displayName":"eric","email":"e@example.com"},"issue":{"id":"issue-uuid","identifier":"MOB-99","title":"Issue"}}}}}`)
		default:
			t.Errorf("unexpected query: %s", req.Query)
			http.Error(w, "unexpected query", http.StatusBadRequest)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("LINEAR_BASE_URL", srv.URL)
	t.Setenv("LINEAR_API_KEY", "test-token")

	out, err := executeRootForTestWithInput(body, "comments", "add", "--issue", "MOB-99", "--body-stdin", "--agent", "--data-source", "live")
	if err != nil {
		t.Fatalf("comments add failed: %v\n%s", err, out)
	}
	if seenBody != body {
		t.Fatalf("body sent to GraphQL = %q, want literal %q", seenBody, body)
	}
}

func TestSimilarAgentOutputsJSON(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "linear.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	raw := json.RawMessage(`{"id":"issue-1","identifier":"SYMPH-309","title":"Headless follow-ups","description":"body"}`)
	if err := db.UpsertIssue("issue-1", "SYMPH-309", "Headless follow-ups", raw); err != nil {
		t.Fatalf("UpsertIssue: %v", err)
	}

	out, err := executeRootForTest("similar", "SYMPH-309", "--db", dbPath, "--agent")
	if err != nil {
		t.Fatalf("similar --agent failed: %v\n%s", err, out)
	}
	var results []map[string]any
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		t.Fatalf("similar --agent output is not JSON: %v\n%s", err, out)
	}
	if len(results) != 1 || results[0]["identifier"] != "SYMPH-309" {
		t.Fatalf("unexpected similar results: %s", out)
	}
}

func TestCommentsListKeepsBodiesInAgentMode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req client.GraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		switch {
		case strings.Contains(req.Query, "issues(filter"):
			fmt.Fprint(w, `{"data":{"issues":{"nodes":[{"id":"issue-uuid"}]}}}`)
		case strings.Contains(req.Query, "comments(first"):
			fmt.Fprint(w, `{"data":{"issue":{"id":"issue-uuid","identifier":"MOB-99","title":"Issue","comments":{"nodes":[{"id":"comment-1","body":"full comment body","createdAt":"2026-06-09T00:00:00Z","updatedAt":"2026-06-09T00:00:00Z","user":{"id":"user-1","name":"eric"}}],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`)
		default:
			t.Errorf("unexpected query: %s", req.Query)
			http.Error(w, "unexpected query", http.StatusBadRequest)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("LINEAR_BASE_URL", srv.URL)
	t.Setenv("LINEAR_API_KEY", "test-token")

	out, err := executeRootForTest("comments", "list", "--issue", "MOB-99", "--agent", "--data-source", "live")
	if err != nil {
		t.Fatalf("comments list failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "full comment body") {
		t.Fatalf("agent output stripped comment body: %s", out)
	}
}

func TestLiveReadCommandsClassifyAPIErrors(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		args       []string
		wantCode   int
	}{
		{
			name:       "comments list auth",
			statusCode: http.StatusUnauthorized,
			args:       []string{"comments", "list", "--issue", "00000000-0000-0000-0000-000000000000", "--agent", "--data-source", "live"},
			wantCode:   4,
		},
		{
			name:       "documents read not found",
			statusCode: http.StatusNotFound,
			args:       []string{"documents", "missing-doc", "--agent", "--data-source", "live"},
			wantCode:   3,
		},
		{
			name:       "documents list rate limit",
			statusCode: http.StatusTooManyRequests,
			args:       []string{"documents", "list", "--agent", "--data-source", "live"},
			wantCode:   7,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, http.StatusText(tt.statusCode), tt.statusCode)
			}))
			t.Cleanup(srv.Close)
			t.Setenv("LINEAR_BASE_URL", srv.URL)
			t.Setenv("LINEAR_API_KEY", "test-token")

			out, err := executeRootForTest(tt.args...)
			if err == nil {
				t.Fatalf("command succeeded unexpectedly:\n%s", out)
			}
			if got := ExitCode(err); got != tt.wantCode {
				t.Fatalf("ExitCode() = %d, want %d; err=%v\n%s", got, tt.wantCode, err, out)
			}
		})
	}
}

func TestWriteCommandsClassifyResolverAPIErrors(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		args       []string
		wantCode   int
	}{
		{
			name:       "comments add issue resolver auth",
			statusCode: http.StatusUnauthorized,
			args:       []string{"comments", "add", "--issue", "MOB-99", "--body", "hello", "--agent", "--data-source", "live"},
			wantCode:   4,
		},
		{
			name:       "issues edit resolver rate limit",
			statusCode: http.StatusTooManyRequests,
			args:       []string{"issues", "edit", "MOB-99", "--title", "Updated", "--agent", "--data-source", "live"},
			wantCode:   7,
		},
		{
			name:       "documents create parent resolver auth",
			statusCode: http.StatusUnauthorized,
			args:       []string{"documents", "create", "--title", "Doc", "--issue", "MOB-99", "--content", "body", "--agent", "--data-source", "live"},
			wantCode:   4,
		},
		{
			name:       "documents edit lookup rate limit",
			statusCode: http.StatusTooManyRequests,
			args:       []string{"documents", "edit", "00000000-0000-0000-0000-000000000000", "--title", "Updated", "--agent", "--data-source", "live"},
			wantCode:   7,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, http.StatusText(tt.statusCode), tt.statusCode)
			}))
			t.Cleanup(srv.Close)
			t.Setenv("LINEAR_BASE_URL", srv.URL)
			t.Setenv("LINEAR_API_KEY", "test-token")

			out, err := executeRootForTest(tt.args...)
			if err == nil {
				t.Fatalf("command succeeded unexpectedly:\n%s", out)
			}
			if got := ExitCode(err); got != tt.wantCode {
				t.Fatalf("ExitCode() = %d, want %d; err=%v\n%s", got, tt.wantCode, err, out)
			}
		})
	}
}

func TestIssuesEditPriorityZeroIsSent(t *testing.T) {
	var seenInput map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req client.GraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if !strings.Contains(req.Query, "issueUpdate") {
			t.Errorf("unexpected query: %s", req.Query)
			http.Error(w, "unexpected query", http.StatusBadRequest)
			return
		}
		seenInput, _ = req.Variables["input"].(map[string]any)
		fmt.Fprint(w, `{"data":{"issueUpdate":{"success":true,"issue":{"id":"00000000-0000-0000-0000-000000000000","identifier":"MOB-99","title":"Issue","description":"","url":"https://linear.app/issue/MOB-99","priority":0,"state":{"id":"state-1","name":"Todo","type":"unstarted"},"team":{"id":"team-1","key":"MOB","name":"Mobilyze"}}}}}`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("LINEAR_BASE_URL", srv.URL)
	t.Setenv("LINEAR_API_KEY", "test-token")

	out, err := executeRootForTest("issues", "edit", "00000000-0000-0000-0000-000000000000", "--priority", "0", "--agent", "--data-source", "live")
	if err != nil {
		t.Fatalf("issues edit failed: %v\n%s", err, out)
	}
	if _, ok := seenInput["priority"]; !ok {
		t.Fatalf("priority was not sent in issueUpdate input: %#v", seenInput)
	}
	if got := seenInput["priority"]; got != float64(0) {
		t.Fatalf("priority = %#v, want 0", got)
	}
}

func executeRootForTest(args ...string) (string, error) {
	return executeRootForTestWithInput("", args...)
}

func executeRootForTestWithInput(input string, args ...string) (string, error) {
	var flags rootFlags
	cmd := newRootCmd(&flags)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if input != "" {
		cmd.SetIn(strings.NewReader(input))
	}
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}
