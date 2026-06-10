package store

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestIssueSearchFTSQueryQuotesIssueKeysAndHyphenatedTerms(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"SYMPH-309": `"SYMPH-309"`,
		"headless Codex worker hardening follow-ups": `"headless" "Codex" "worker" "hardening" "follow-ups"`,
		"---": "",
	}
	for input, want := range cases {
		if got := IssueSearchFTSQuery(input); got != want {
			t.Fatalf("IssueSearchFTSQuery(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSearchIssuesAcceptsIssueKeysAndHyphenatedProse(t *testing.T) {
	t.Parallel()
	db, err := Open(filepath.Join(t.TempDir(), "linear.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	raw, err := json.Marshal(map[string]any{
		"id":          "issue-1",
		"identifier":  "SYMPH-309",
		"title":       "Headless Codex worker hardening follow-ups",
		"description": "Regression body mentioning follow-ups and shell expansions.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertIssue("issue-1", "SYMPH-309", "Headless Codex worker hardening follow-ups", raw); err != nil {
		t.Fatalf("UpsertIssue: %v", err)
	}

	for _, query := range []string{"SYMPH-309", "headless Codex worker hardening follow-ups"} {
		results, err := db.SearchIssues(query)
		if err != nil {
			t.Fatalf("SearchIssues(%q) returned error: %v", query, err)
		}
		if len(results) != 1 {
			t.Fatalf("SearchIssues(%q) returned %d results, want 1", query, len(results))
		}
	}
}

func TestSearchIssuesPreservesFTSRankOrdering(t *testing.T) {
	t.Parallel()
	db, err := Open(filepath.Join(t.TempDir(), "linear.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	lessRelevant, err := json.Marshal(map[string]any{
		"id":          "issue-1",
		"identifier":  "MOB-LOW",
		"title":       "Pipeline token burn",
		"description": "Pipeline token burn",
	})
	if err != nil {
		t.Fatal(err)
	}
	moreRelevant, err := json.Marshal(map[string]any{
		"id":          "issue-2",
		"identifier":  "MOB-HIGH",
		"title":       "Pipeline token burn pipeline token burn pipeline token burn",
		"description": "Pipeline token burn pipeline token burn pipeline token burn",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertIssue("issue-1", "MOB-LOW", "Pipeline token burn", lessRelevant); err != nil {
		t.Fatalf("UpsertIssue low: %v", err)
	}
	if err := db.UpsertIssue("issue-2", "MOB-HIGH", "Pipeline token burn pipeline token burn pipeline token burn", moreRelevant); err != nil {
		t.Fatalf("UpsertIssue high: %v", err)
	}

	results, err := db.SearchIssues("pipeline token burn")
	if err != nil {
		t.Fatalf("SearchIssues returned error: %v", err)
	}
	if len(results) < 2 {
		t.Fatalf("SearchIssues returned %d results, want at least 2", len(results))
	}
	var first struct {
		Identifier string `json:"identifier"`
	}
	if err := json.Unmarshal(results[0], &first); err != nil {
		t.Fatalf("unmarshal first result: %v", err)
	}
	if first.Identifier != "MOB-HIGH" {
		t.Fatalf("first result = %s, want MOB-HIGH; results=%s", first.Identifier, results)
	}
}
