package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mvanhorn/printing-press-library/library/project-management/linear/internal/client"
	"github.com/mvanhorn/printing-press-library/library/project-management/linear/internal/store"
)

const workflowStatesResponse = `{"data":{"workflowStates":{"nodes":[
	{"id":"state-todo","name":"Todo","type":"unstarted","color":"#aaa","position":1,"team":{"id":"team-1","key":"SYMPH","name":"Symphony"}},
	{"id":"state-progress","name":"In Progress","type":"started","color":"#bbb","position":2,"team":{"id":"team-1","key":"SYMPH","name":"Symphony"}}
]}}}`

func TestWorkflowStatesListLiveIncludesIDs(t *testing.T) {
	var seenFilter map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req client.GraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if !strings.Contains(req.Query, "workflowStates(") {
			t.Errorf("unexpected query: %s", req.Query)
			http.Error(w, "unexpected query", http.StatusBadRequest)
			return
		}
		seenFilter, _ = req.Variables["filter"].(map[string]any)
		fmt.Fprint(w, workflowStatesResponse)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("LINEAR_BASE_URL", srv.URL)
	t.Setenv("LINEAR_API_KEY", "test-token")

	out, err := executeRootForTest("workflow-states", "list", "--team", "SYMPH",
		"--db", filepath.Join(t.TempDir(), "linear.db"),
		"--agent", "--data-source", "live", "--select", "id,name,type")
	if err != nil {
		t.Fatalf("workflow-states list failed: %v\n%s", err, out)
	}
	var rows []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("output is not a JSON array: %v\n%s", err, out)
	}
	if len(rows) != 2 || rows[0].ID == "" || rows[1].ID == "" {
		t.Fatalf("expected 2 states with ids, got: %s", out)
	}
	team, _ := seenFilter["team"].(map[string]any)
	if team == nil {
		t.Fatalf("team filter was not sent to GraphQL: %v", seenFilter)
	}
}

func TestStatesListAlias(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, workflowStatesResponse)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("LINEAR_BASE_URL", srv.URL)
	t.Setenv("LINEAR_API_KEY", "test-token")

	out, err := executeRootForTest("states", "list", "--team", "SYMPH",
		"--db", filepath.Join(t.TempDir(), "linear.db"),
		"--agent", "--data-source", "live")
	if err != nil {
		t.Fatalf("states list alias failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "state-progress") {
		t.Fatalf("alias output missing state id: %s", out)
	}
}

func TestWorkflowStatesListLocalStore(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "linear.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.UpsertTeam("team-1", json.RawMessage(`{"id":"team-1","key":"SYMPH","name":"Symphony"}`)); err != nil {
		t.Fatalf("UpsertTeam: %v", err)
	}
	states := []string{
		`{"id":"state-todo","name":"Todo","type":"unstarted","position":1,"team":{"id":"team-1","key":"SYMPH","name":"Symphony"}}`,
		`{"id":"state-done","name":"Done","type":"completed","position":5,"team":{"id":"team-1","key":"SYMPH","name":"Symphony"}}`,
		`{"id":"state-other","name":"Todo","type":"unstarted","position":1,"team":{"id":"team-2","key":"MOB","name":"Mobilyze"}}`,
	}
	for _, s := range states {
		var meta struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(s), &meta); err != nil {
			t.Fatal(err)
		}
		if err := db.UpsertWorkflowState(meta.ID, json.RawMessage(s)); err != nil {
			t.Fatalf("UpsertWorkflowState: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	out, err := executeRootForTest("workflow-states", "list", "--team", "SYMPH",
		"--db", dbPath, "--agent", "--data-source", "local", "--select", "id,name,type")
	if err != nil {
		t.Fatalf("workflow-states list local failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "state-todo") || !strings.Contains(out, "state-done") {
		t.Fatalf("local output missing SYMPH states: %s", out)
	}
	if strings.Contains(out, "state-other") {
		t.Fatalf("team filter leaked other team's states: %s", out)
	}
}

func TestIssuesGetByIdentifierSelectsStateID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req client.GraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if !strings.Contains(req.Query, "state { id name type }") {
			t.Errorf("identifier-path issue query does not request state.id: %s", req.Query)
		}
		fmt.Fprint(w, `{"data":{"issues":{"nodes":[{"id":"issue-uuid","identifier":"SYMPH-331","title":"Issue","state":{"id":"state-progress","name":"In Progress","type":"started"},"team":{"id":"team-1","key":"SYMPH","name":"Symphony"}}]}}}`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("LINEAR_BASE_URL", srv.URL)
	t.Setenv("LINEAR_API_KEY", "test-token")

	out, err := executeRootForTest("issues", "SYMPH-331",
		"--db", filepath.Join(t.TempDir(), "linear.db"),
		"--agent", "--data-source", "live",
		"--select", "identifier,state.id,state.name,state.type")
	if err != nil {
		t.Fatalf("issues get failed: %v\n%s", err, out)
	}
	var got struct {
		Results struct {
			Identifier string `json:"identifier"`
			State      struct {
				ID   string `json:"id"`
				Name string `json:"name"`
				Type string `json:"type"`
			} `json:"state"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if got.Results.State.ID != "state-progress" {
		t.Fatalf("--select state.id did not include the state UUID: %s", out)
	}
}

func TestNormalizeDocumentRef(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"4a09c2e6-3a25-4cb8-ab63-9c9f6754b24e", "4a09c2e6-3a25-4cb8-ab63-9c9f6754b24e"},
		{"f7f48ab36080", "f7f48ab36080"},
		{"symphony-pipeline-restart-runbook-f7f48ab36080", "f7f48ab36080"},
		{"https://linear.app/mobilyze-llc/document/symphony-pipeline-restart-runbook-f7f48ab36080", "f7f48ab36080"},
		{"https://linear.app/mobilyze-llc/document/symphony-pipeline-restart-runbook-f7f48ab36080?view=full", "f7f48ab36080"},
		{"  f7f48ab36080  ", "f7f48ab36080"},
	}
	for _, c := range cases {
		if got := normalizeDocumentRef(c.in); got != c.want {
			t.Errorf("normalizeDocumentRef(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDocumentsAcceptsFullURLSlug(t *testing.T) {
	var seenSlug string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req client.GraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		seenSlug, _ = req.Variables["slug"].(string)
		fmt.Fprint(w, `{"data":{"documents":{"nodes":[{"id":"doc-uuid","title":"Runbook","slugId":"f7f48ab36080","content":"runbook body"}]}}}`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("LINEAR_BASE_URL", srv.URL)
	t.Setenv("LINEAR_API_KEY", "test-token")

	out, err := executeRootForTest("documents", "symphony-pipeline-restart-runbook-f7f48ab36080", "--agent")
	if err != nil {
		t.Fatalf("documents lookup by full slug failed: %v\n%s", err, out)
	}
	if seenSlug != "f7f48ab36080" {
		t.Fatalf("slug sent to GraphQL = %q, want normalized %q", seenSlug, "f7f48ab36080")
	}
	if !strings.Contains(out, "doc-uuid") {
		t.Fatalf("document output missing id: %s", out)
	}
}

func TestCommentsListPositionalIssue(t *testing.T) {
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
			fmt.Fprint(w, `{"data":{"issue":{"id":"issue-uuid","identifier":"SYMPH-317","title":"Issue","comments":{"nodes":[{"id":"comment-1","body":"latest update"}],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`)
		default:
			t.Errorf("unexpected query: %s", req.Query)
			http.Error(w, "unexpected query", http.StatusBadRequest)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("LINEAR_BASE_URL", srv.URL)
	t.Setenv("LINEAR_API_KEY", "test-token")

	out, err := executeRootForTest("comments", "list", "SYMPH-317", "--agent", "--data-source", "live")
	if err != nil {
		t.Fatalf("comments list positional failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "latest update") {
		t.Fatalf("positional comments list missing comment body: %s", out)
	}
}

func TestCommentsListIssueRequiredAndConflicts(t *testing.T) {
	_, err := executeRootForTest("comments", "list", "--agent")
	if err == nil || ExitCode(err) != 2 {
		t.Fatalf("missing issue should be a code-2 usage error, got %v", err)
	}

	_, err = executeRootForTest("comments", "list", "SYMPH-317", "--issue", "SYMPH-1", "--agent")
	if err == nil || ExitCode(err) != 2 {
		t.Fatalf("conflicting positional/--issue should be a code-2 usage error, got %v", err)
	}

	// Same value both ways is not a conflict — needs a live endpoint, so only
	// assert it is not rejected as a usage error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req client.GraphQLRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if strings.Contains(req.Query, "issues(filter") {
			fmt.Fprint(w, `{"data":{"issues":{"nodes":[{"id":"issue-uuid"}]}}}`)
			return
		}
		fmt.Fprint(w, `{"data":{"issue":{"id":"issue-uuid","identifier":"SYMPH-317","title":"Issue","comments":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`)
	}))
	defer srv.Close()
	t.Setenv("LINEAR_BASE_URL", srv.URL)
	t.Setenv("LINEAR_API_KEY", "test-token")
	if _, err := executeRootForTest("comments", "list", "SYMPH-317", "--issue", "SYMPH-317", "--agent", "--data-source", "live"); err != nil {
		t.Fatalf("matching positional/--issue should be accepted, got %v", err)
	}
}

func TestIssuesEditStateNameResolvesUUID(t *testing.T) {
	var seenStateID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req client.GraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		switch {
		case strings.Contains(req.Query, "issues(filter"):
			fmt.Fprint(w, `{"data":{"issues":{"nodes":[{"id":"issue-uuid","identifier":"MOB-105","title":"Issue","description":"","state":{"id":"state-todo","name":"Todo","type":"unstarted"},"team":{"id":"team-1","key":"MOB","name":"Mobilyze"}}]}}}`)
		case strings.Contains(req.Query, "workflowStates("):
			fmt.Fprint(w, `{"data":{"workflowStates":{"nodes":[{"id":"state-progress","name":"In Progress","type":"started"}]}}}`)
		case strings.Contains(req.Query, "issueUpdate"):
			input, _ := req.Variables["input"].(map[string]any)
			seenStateID, _ = input["stateId"].(string)
			fmt.Fprint(w, `{"data":{"issueUpdate":{"success":true,"issue":{"id":"issue-uuid","identifier":"MOB-105","title":"Issue","state":{"id":"state-progress","name":"In Progress","type":"started"},"team":{"id":"team-1","key":"MOB","name":"Mobilyze"}}}}}`)
		default:
			t.Errorf("unexpected query: %s", req.Query)
			http.Error(w, "unexpected query", http.StatusBadRequest)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("LINEAR_BASE_URL", srv.URL)
	t.Setenv("LINEAR_API_KEY", "test-token")

	out, err := executeRootForTest("issues", "edit", "MOB-105", "--state-name", "In Progress",
		"--db", filepath.Join(t.TempDir(), "linear.db"), "--agent")
	if err != nil {
		t.Fatalf("issues edit --state-name failed: %v\n%s", err, out)
	}
	if seenStateID != "state-progress" {
		t.Fatalf("stateId sent to issueUpdate = %q, want resolved %q", seenStateID, "state-progress")
	}
}

func TestIssuesEditStateTypeAmbiguousIsUsageError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req client.GraphQLRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch {
		case strings.Contains(req.Query, "issues(filter"):
			fmt.Fprint(w, `{"data":{"issues":{"nodes":[{"id":"issue-uuid","identifier":"MOB-105","title":"Issue","description":"","team":{"id":"team-1","key":"MOB","name":"Mobilyze"}}]}}}`)
		case strings.Contains(req.Query, "workflowStates("):
			fmt.Fprint(w, `{"data":{"workflowStates":{"nodes":[{"id":"s1","name":"In Progress","type":"started"},{"id":"s2","name":"In Review","type":"started"}]}}}`)
		default:
			t.Errorf("unexpected query: %s", req.Query)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("LINEAR_BASE_URL", srv.URL)
	t.Setenv("LINEAR_API_KEY", "test-token")

	_, err := executeRootForTest("issues", "edit", "MOB-105", "--state-type", "started",
		"--db", filepath.Join(t.TempDir(), "linear.db"), "--agent")
	if err == nil || ExitCode(err) != 2 {
		t.Fatalf("ambiguous --state-type should be a code-2 usage error, got %v", err)
	}
	if !strings.Contains(err.Error(), "In Review") {
		t.Fatalf("ambiguity error should list candidates, got: %v", err)
	}
}

func TestIssuesEditStateFlagValidation(t *testing.T) {
	t.Parallel()
	_, err := executeRootForTest("issues", "edit", "MOB-105", "--state", "In Progress", "--agent", "--dry-run")
	if err == nil || ExitCode(err) != 2 {
		t.Fatalf("--state with a non-UUID should be a code-2 usage error, got %v", err)
	}

	_, err = executeRootForTest("issues", "edit", "MOB-105",
		"--state", "11111111-2222-3333-4444-555555555555", "--state-name", "Done", "--agent", "--dry-run")
	if err == nil || ExitCode(err) != 2 {
		t.Fatalf("--state plus --state-name should be a code-2 usage error, got %v", err)
	}
}

func TestFinalizeErrorEmitsJSONEnvelope(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	flags := &rootFlags{agent: true, asJSON: true}
	finalizeError(flags, nil, &stdout, &stderr, notFoundErr(fmt.Errorf("document \"missing-doc\" not found")))

	var envelope struct {
		Error string `json:"error"`
		Code  int    `json:"code"`
		Type  string `json:"type"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("agent error output is not JSON: %v\n%s", err, stdout.String())
	}
	if envelope.Code != 3 || envelope.Type != "not_found" || !strings.Contains(envelope.Error, "missing-doc") {
		t.Fatalf("unexpected envelope: %+v", envelope)
	}
	if stderr.Len() != 0 {
		t.Fatalf("agent mode should not write plain text to stderr: %s", stderr.String())
	}
}

func TestFinalizeErrorUsageEnvelopeAndArgFallback(t *testing.T) {
	t.Parallel()
	// Flags unparsed (e.g. unknown-flag failure) — detection falls back to raw args.
	var stdout, stderr bytes.Buffer
	finalizeError(&rootFlags{}, []string{"comments", "list", "--agent"}, &stdout, &stderr, usageErr(fmt.Errorf("--issue is required")))
	var envelope struct {
		Code int    `json:"code"`
		Type string `json:"type"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("usage error output is not JSON: %v\n%s", err, stdout.String())
	}
	if envelope.Code != 2 || envelope.Type != "usage" {
		t.Fatalf("unexpected usage envelope: %+v", envelope)
	}
}

func TestFinalizeErrorPlainModeWritesStderr(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	finalizeError(&rootFlags{}, []string{"issues", "MOB-1"}, &stdout, &stderr, notFoundErr(fmt.Errorf("issue not found")))
	if stdout.Len() != 0 {
		t.Fatalf("plain mode should not write to stdout: %s", stdout.String())
	}
	if !strings.HasPrefix(stderr.String(), "Error: ") {
		t.Fatalf("plain mode should keep the Error: prefix, got: %s", stderr.String())
	}
}

func TestFinalizeErrorSkipsDoubleEnvelope(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	flags := &rootFlags{agent: true, asJSON: true, envelopeEmitted: true}
	finalizeError(flags, nil, &stdout, &stderr, apiErr(fmt.Errorf("HTTP 409 conflict")))
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("already-emitted envelope should not be duplicated: stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
}
