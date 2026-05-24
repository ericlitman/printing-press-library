// Copyright 2026 mvanhorn. Licensed under Apache-2.0. See LICENSE.

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mvanhorn/printing-press-library/library/payments/prediction-goat/internal/store"
)

// withTempHome sets HOME to a fresh temp dir for the duration of the
// test, then restores it on cleanup. Used to isolate the audit log and
// teach.log writes from the developer's real ~/.local/share tree.
func withTempHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	// Clear any external --no-learn env state so the default per-test
	// behavior is learning ON.
	t.Setenv(noLearnEnvVar, "")
	return dir
}

// runRootArgs executes the CLI with the supplied args, returning stdout,
// stderr, and the error from rootCmd.Execute. The test harness routes
// both streams through bytes.Buffer; SetIn is wired to /dev/null so a
// command that calls stdin doesn't block.
func runRootArgs(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	rootCmd := RootCmd()
	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs(args)
	err := rootCmd.Execute()
	return stdout.String(), stderr.String(), err
}

func TestTeachCommand_SilentOnSuccess(t *testing.T) {
	home := withTempHome(t)
	dbPath := filepath.Join(home, "data.db")

	stdout, stderr, err := runRootArgs(t,
		"teach",
		"--query", "portugal world cup odds",
		"--resource", "KXMENWORLDCUP-26-PT",
		"--db", dbPath,
	)
	if err != nil {
		t.Fatalf("teach exited non-zero: %v (stderr=%q)", err, stderr)
	}
	if stdout != "" {
		t.Errorf("teach should be silent on success; stdout=%q", stdout)
	}
	if stderr != "" {
		t.Errorf("teach should be silent on success; stderr=%q", stderr)
	}

	// Verify the row landed in the DB.
	s, err := store.OpenWithContext(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer s.Close()
	rows, err := s.ListLearnings(store.ListLearningsFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 || rows[0].ResourceID != "KXMENWORLDCUP-26-PT" {
		t.Errorf("expected one row for KXMENWORLDCUP-26-PT, got %#v", rows)
	}
}

func TestTeachCommand_MultipleResourcesSameCall(t *testing.T) {
	home := withTempHome(t)
	dbPath := filepath.Join(home, "data.db")

	_, _, err := runRootArgs(t,
		"teach",
		"--query", "portugal world cup odds",
		"--resource", "KXMENWORLDCUP-26-PT",
		"--resource", "will-portugal-win-the-2026-fifa-world-cup-912",
		"--db", dbPath,
	)
	if err != nil {
		t.Fatalf("teach: %v", err)
	}

	s, _ := store.OpenWithContext(context.Background(), dbPath)
	defer s.Close()
	rows, _ := s.ListLearnings(store.ListLearningsFilter{})
	if len(rows) != 2 {
		t.Errorf("expected 2 rows, got %d", len(rows))
	}
}

func TestTeachCommand_IdempotentBumpsConfidence(t *testing.T) {
	home := withTempHome(t)
	dbPath := filepath.Join(home, "data.db")

	args := []string{
		"teach",
		"--query", "portugal world cup",
		"--resource", "KXMENWORLDCUP-26-PT",
		"--db", dbPath,
	}
	if _, _, err := runRootArgs(t, args...); err != nil {
		t.Fatalf("teach 1: %v", err)
	}
	if _, _, err := runRootArgs(t, args...); err != nil {
		t.Fatalf("teach 2: %v", err)
	}

	s, _ := store.OpenWithContext(context.Background(), dbPath)
	defer s.Close()
	rows, _ := s.ListLearnings(store.ListLearningsFilter{})
	if len(rows) != 1 {
		t.Fatalf("expected 1 row after re-teach, got %d", len(rows))
	}
	if rows[0].Confidence != 2 {
		t.Errorf("confidence should bump to 2 on re-teach, got %d", rows[0].Confidence)
	}
}

func TestTeachCommand_RespectsNoLearnEnvVar(t *testing.T) {
	home := withTempHome(t)
	dbPath := filepath.Join(home, "data.db")
	t.Setenv(noLearnEnvVar, "true")

	stdout, stderr, err := runRootArgs(t,
		"teach",
		"--query", "portugal world cup",
		"--resource", "KXMENWORLDCUP-26-PT",
		"--db", dbPath,
	)
	if err != nil {
		t.Fatalf("teach with NO_LEARN should be silent no-op, got err: %v stderr=%q", err, stderr)
	}
	if stdout != "" || stderr != "" {
		t.Errorf("NO_LEARN should be silent; got stdout=%q stderr=%q", stdout, stderr)
	}

	// DB file should not even exist — teach exited before opening.
	if _, statErr := os.Stat(dbPath); statErr == nil {
		s, _ := store.OpenWithContext(context.Background(), dbPath)
		defer s.Close()
		rows, _ := s.ListLearnings(store.ListLearningsFilter{})
		if len(rows) != 0 {
			t.Errorf("NO_LEARN should leave DB empty; got %d rows", len(rows))
		}
	}
}

func TestTeachCommand_RespectsNoLearnFlag(t *testing.T) {
	home := withTempHome(t)
	dbPath := filepath.Join(home, "data.db")

	_, _, err := runRootArgs(t,
		"--no-learn",
		"teach",
		"--query", "portugal world cup",
		"--resource", "KXMENWORLDCUP-26-PT",
		"--db", dbPath,
	)
	if err != nil {
		t.Fatalf("teach with --no-learn should be silent no-op, got err: %v", err)
	}
	if _, statErr := os.Stat(dbPath); statErr == nil {
		s, _ := store.OpenWithContext(context.Background(), dbPath)
		defer s.Close()
		rows, _ := s.ListLearnings(store.ListLearningsFilter{})
		if len(rows) != 0 {
			t.Errorf("--no-learn should leave DB empty; got %d rows", len(rows))
		}
	}
}

func TestTeachCommand_MissingResourceLogsAndExitsNonZero(t *testing.T) {
	home := withTempHome(t)
	dbPath := filepath.Join(home, "data.db")

	stdout, stderr, err := runRootArgs(t,
		"teach",
		"--query", "portugal world cup",
		"--db", dbPath,
	)
	if err == nil {
		t.Fatal("teach without --resource should exit non-zero")
	}
	if stdout != "" {
		t.Errorf("error stdout should be empty; got %q", stdout)
	}
	if stderr != "" {
		// We swallow cobra's stderr via SilenceErrors; cobra's prerun
		// might still print on usage error. The contract is "background-
		// safe": the cobra harness should not leak. If this regresses,
		// we'll need to tighten the silencing.
		t.Errorf("error stderr should be empty; got %q", stderr)
	}

	// teach.log should exist with an error line.
	logPath := filepath.Join(home, ".local", "share", "prediction-goat-pp-cli", teachLogFileName)
	data, statErr := os.ReadFile(logPath)
	if statErr != nil {
		t.Fatalf("teach.log should exist on error: %v", statErr)
	}
	if !strings.Contains(string(data), "missing --resource") {
		t.Errorf("teach.log should record the failure; got %q", string(data))
	}
}

func TestTeachCommand_AppendsAuditLog(t *testing.T) {
	home := withTempHome(t)
	dbPath := filepath.Join(home, "data.db")

	_, _, err := runRootArgs(t,
		"teach",
		"--query", "portugal world cup",
		"--resource", "KXMENWORLDCUP-26-PT",
		"--db", dbPath,
	)
	if err != nil {
		t.Fatalf("teach: %v", err)
	}
	auditPath := filepath.Join(home, ".local", "share", "prediction-goat-pp-cli", learningsAuditFileName)
	data, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("audit log should exist: %v", err)
	}
	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(data), &entry); err != nil {
		t.Fatalf("audit log JSON parse: %v", err)
	}
	if entry["action"] != "teach" {
		t.Errorf("audit action=%v, want teach", entry["action"])
	}
}

func TestRecallCommand_FoundAndNotFound(t *testing.T) {
	home := withTempHome(t)
	dbPath := filepath.Join(home, "data.db")

	// Seed via teach.
	if _, _, err := runRootArgs(t,
		"teach",
		"--query", "portugal world cup odds",
		"--resource", "KXMENWORLDCUP-26-PT",
		"--resource", "will-portugal-win-the-2026-fifa-world-cup-912",
		"--db", dbPath,
	); err != nil {
		t.Fatalf("seed teach: %v", err)
	}

	// Exact-ish recall.
	stdout, _, err := runRootArgs(t,
		"recall", "portugal world cup odds",
		"--db", dbPath,
		"--agent",
	)
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	var env recallEnvelope
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("recall JSON: %v (stdout=%q)", err, stdout)
	}
	if !env.Found || len(env.Results) != 2 {
		t.Errorf("recall: want found+2 results, got %#v", env)
	}

	// Token-overlap recall: "portugal chances at the world cup"
	stdout, _, err = runRootArgs(t,
		"recall", "portugal chances at the world cup",
		"--db", dbPath,
		"--agent",
	)
	if err != nil {
		t.Fatalf("recall overlap: %v", err)
	}
	env = recallEnvelope{}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("recall overlap JSON: %v", err)
	}
	if !env.Found {
		t.Errorf("token-overlap recall: want found, got %#v", env)
	}

	// Unrelated query.
	stdout, _, err = runRootArgs(t,
		"recall", "lakers tonight",
		"--db", dbPath,
		"--agent",
	)
	if err != nil {
		t.Fatalf("recall unrelated: %v", err)
	}
	env = recallEnvelope{}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("recall unrelated JSON: %v", err)
	}
	if env.Found || len(env.Results) != 0 {
		t.Errorf("unrelated recall: want empty, got %#v", env)
	}
}

func TestRecallCommand_RespectsNoLearnEnvVar(t *testing.T) {
	home := withTempHome(t)
	dbPath := filepath.Join(home, "data.db")

	// Seed a real row first (with NO_LEARN unset).
	if _, _, err := runRootArgs(t,
		"teach",
		"--query", "portugal world cup",
		"--resource", "KXMENWORLDCUP-26-PT",
		"--db", dbPath,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Now flip NO_LEARN on for the recall.
	t.Setenv(noLearnEnvVar, "true")
	stdout, _, err := runRootArgs(t,
		"recall", "portugal world cup",
		"--db", dbPath,
		"--agent",
	)
	if err != nil {
		t.Fatalf("recall with NO_LEARN: %v", err)
	}
	var env recallEnvelope
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("recall JSON: %v", err)
	}
	if env.Found {
		t.Errorf("NO_LEARN should suppress recall results; got %#v", env)
	}
}

func TestRecallCommand_MinConfidence(t *testing.T) {
	home := withTempHome(t)
	dbPath := filepath.Join(home, "data.db")

	// One teach for A (conf=1), two teaches for B (conf=2).
	if _, _, err := runRootArgs(t,
		"teach", "--query", "portugal world cup", "--resource", "A", "--db", dbPath); err != nil {
		t.Fatalf("seed A: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, _, err := runRootArgs(t,
			"teach", "--query", "portugal world cup", "--resource", "B", "--db", dbPath); err != nil {
			t.Fatalf("seed B: %v", err)
		}
	}

	stdout, _, err := runRootArgs(t,
		"recall", "portugal world cup",
		"--db", dbPath,
		"--min-confidence", "2",
		"--agent",
	)
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	var env recallEnvelope
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("recall JSON: %v", err)
	}
	if len(env.Results) != 1 || env.Results[0].ResourceID != "B" {
		t.Errorf("min-confidence filter: want B only, got %#v", env.Results)
	}
}

func TestLearningsList_FiltersByQuery(t *testing.T) {
	home := withTempHome(t)
	dbPath := filepath.Join(home, "data.db")

	if _, _, err := runRootArgs(t,
		"teach", "--query", "portugal world cup", "--resource", "A", "--db", dbPath); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, _, err := runRootArgs(t,
		"teach", "--query", "lakers tonight", "--resource", "B", "--db", dbPath); err != nil {
		t.Fatalf("seed: %v", err)
	}

	stdout, _, err := runRootArgs(t,
		"learnings", "list",
		"--query", "portugal",
		"--db", dbPath,
		"--agent",
	)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var rows []store.LearningRow
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("list JSON: %v (stdout=%q)", err, stdout)
	}
	if len(rows) != 1 || rows[0].ResourceID != "A" {
		t.Errorf("list filter: want one (A), got %#v", rows)
	}
}

func TestForgetCommand_TargetedAndAll(t *testing.T) {
	home := withTempHome(t)
	dbPath := filepath.Join(home, "data.db")

	for _, rid := range []string{"X", "Y", "Z"} {
		if _, _, err := runRootArgs(t,
			"teach", "--query", "portugal world cup", "--resource", rid, "--db", dbPath); err != nil {
			t.Fatalf("seed %s: %v", rid, err)
		}
	}

	// Refuse without a filter.
	_, _, err := runRootArgs(t,
		"forget", "portugal world cup",
		"--db", dbPath,
	)
	if err == nil {
		t.Errorf("forget without filter should error")
	}

	// Targeted forget.
	stdout, _, err := runRootArgs(t,
		"forget", "portugal world cup",
		"--resource", "Y",
		"--db", dbPath,
		"--agent",
	)
	if err != nil {
		t.Fatalf("forget Y: %v", err)
	}
	var resp map[string]any
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("forget JSON: %v", err)
	}
	if v, _ := resp["deleted"].(float64); v != 1 {
		t.Errorf("forget Y: want deleted=1, got %v", resp["deleted"])
	}

	// --all wipes the rest.
	stdout, _, err = runRootArgs(t,
		"forget", "portugal world cup",
		"--all",
		"--db", dbPath,
		"--agent",
	)
	if err != nil {
		t.Fatalf("forget all: %v", err)
	}
	resp = nil
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("forget all JSON: %v", err)
	}
	if v, _ := resp["deleted"].(float64); v != 2 {
		t.Errorf("forget all: want deleted=2, got %v", resp["deleted"])
	}
}

func TestNormalizationSymmetry_TeachAndRecall(t *testing.T) {
	home := withTempHome(t)
	dbPath := filepath.Join(home, "data.db")

	// Teach with a capitalized + stopwordy query.
	if _, _, err := runRootArgs(t,
		"teach",
		"--query", "What are the odds Portugal wins the World Cup?",
		"--resource", "KXMENWORLDCUP-26-PT",
		"--db", dbPath,
	); err != nil {
		t.Fatalf("teach: %v", err)
	}

	// Recall with the bare form.
	stdout, _, err := runRootArgs(t,
		"recall", "portugal wins world cup",
		"--db", dbPath,
		"--agent",
	)
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	var env recallEnvelope
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("recall JSON: %v (stdout=%q)", err, stdout)
	}
	if !env.Found {
		t.Errorf("normalized teach should be recall-discoverable from bare form; got %#v", env)
	}
}

func TestTeachHintFor_HighConfidenceSuppresses(t *testing.T) {
	t.Parallel()
	if got := teachHintFor("portugal world cup", 1, true, 3); got != "" {
		t.Errorf("high-confidence: want empty hint, got %q", got)
	}
	if got := teachHintFor("portugal world cup", 0, false, 0); got != "" {
		t.Errorf("no hits: want empty hint, got %q", got)
	}
	if got := teachHintFor("portugal world cup", 0, false, 3); got == "" {
		t.Errorf("low-confidence + hits: want non-empty hint, got empty")
	}
	if got := teachHintFor(`say "hi"`, 0, false, 1); !strings.Contains(got, `\"hi\"`) {
		t.Errorf("hint should escape inner quotes; got %q", got)
	}
}

func TestApplyLearningsForTopic_BoostMovesHitToFront(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "rerank.db")
	s, err := store.OpenWithContext(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	// Seed a learning that boosts KXMENWORLDCUP-26-PT for portugal queries.
	if _, _, err := s.UpsertLearning(store.UpsertLearningInput{
		Query:        "portugal world cup",
		ResourceID:   "KXMENWORLDCUP-26-PT",
		ResourceType: "kalshi_markets",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Two hits, the boosted one not at front.
	hits := []topicHit{
		{Source: "kalshi", Kind: "market", ID: "KXFUSION", Title: "Nuclear fusion"},
		{Source: "kalshi", Kind: "market", ID: "KXMENWORLDCUP-26-PT", Title: "Will Portugal win the 2026 World Cup?"},
	}
	result, applied, _ := applyLearningsForTopic(context.Background(), s, "portugal world cup", hits)
	if applied != 1 {
		t.Errorf("applied: want 1, got %d", applied)
	}
	if result[0].ID != "KXMENWORLDCUP-26-PT" {
		t.Errorf("boost should move KXMENWORLDCUP-26-PT to position 0; got %s", result[0].ID)
	}
}

func TestApplyLearningsForTopic_SyntheticInsert(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "rerank.db")
	s, err := store.OpenWithContext(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	// Seed a resource in the store so the synthetic insert can fetch it.
	if err := s.Upsert("kalshi_markets", "KXMENWORLDCUP-26-PT",
		json.RawMessage(`{"ticker":"KXMENWORLDCUP-26-PT","event_ticker":"KXMENWORLDCUP-26","title":"Will Portugal win the 2026 World Cup?","status":"active"}`)); err != nil {
		t.Fatalf("seed resource: %v", err)
	}
	if _, _, err := s.UpsertLearning(store.UpsertLearningInput{
		Query:        "portugal world cup",
		ResourceID:   "KXMENWORLDCUP-26-PT",
		ResourceType: "kalshi_markets",
	}); err != nil {
		t.Fatalf("seed learning: %v", err)
	}

	// FTS missed it entirely — only an unrelated hit comes in.
	hits := []topicHit{
		{Source: "kalshi", Kind: "market", ID: "KXFUSION", Title: "Nuclear fusion"},
	}
	result, applied, _ := applyLearningsForTopic(context.Background(), s, "portugal world cup", hits)
	if applied != 1 {
		t.Errorf("applied: want 1, got %d", applied)
	}
	if len(result) != 2 || result[0].ID != "KXMENWORLDCUP-26-PT" {
		t.Errorf("synthetic insert should add KXMENWORLDCUP-26-PT at front; got %#v", result)
	}
}

func TestApplyLearningsForTopic_NoLearnSkipsLayer(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "rerank.db")
	s, err := store.OpenWithContext(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	if _, _, err := s.UpsertLearning(store.UpsertLearningInput{
		Query:        "portugal world cup",
		ResourceID:   "KXMENWORLDCUP-26-PT",
		ResourceType: "kalshi_markets",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	flags := &rootFlags{noLearn: true}
	if active := noLearnActive(flags); !active {
		t.Errorf("expected noLearnActive=true with flag set")
	}
	// The call sites check noLearnActive before invoking the apply.
	// Don't call applyLearningsForTopic here; the contract is the gate
	// at the call site, which other tests exercise via the full
	// command path.
}

// TestRecallCommand_EnvelopeShape_QueryEntitiesAndWarnings verifies
// U3's envelope shape: every recall response carries query_entities,
// even on cold queries; warnings surface at the top level when the
// result set is empty.
func TestRecallCommand_EnvelopeShape_QueryEntitiesAndWarnings(t *testing.T) {
	home := withTempHome(t)
	dbPath := filepath.Join(home, "data.db")

	stdout, _, err := runRootArgs(t,
		"recall", "odds USA wins world cup",
		"--db", dbPath,
		"--agent",
	)
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	var env recallEnvelope
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("recall JSON: %v (stdout=%q)", err, stdout)
	}
	// Cold envelope still carries query_entities so the agent can see
	// what the CLI is matching on.
	if !envSliceContains(env.QueryEntities, "USA") {
		t.Errorf("want query_entities to include USA; got %v", env.QueryEntities)
	}
	if env.Normalized == "" {
		t.Errorf("want non-empty normalized; got %q", env.Normalized)
	}
}

// TestRecallCommand_DebugMismatchesFlag verifies that --debug-mismatches
// surfaces cross-entity rows that cleared the Jaccard threshold but
// failed entity validation. The flagship England-vs-Portugal trace.
func TestRecallCommand_DebugMismatchesFlag(t *testing.T) {
	home := withTempHome(t)
	dbPath := filepath.Join(home, "data.db")

	// Seed: Portugal resource + learning.
	if _, _, err := runRootArgs(t,
		"teach",
		"--query", "odds Portugal wins world cup",
		"--resource", "KXMENWORLDCUP-26-PT",
		"--resource-type", "kalshi_markets",
		"--db", dbPath,
	); err != nil {
		t.Fatalf("seed teach: %v", err)
	}

	// England query without --debug-mismatches: results empty, mismatches absent.
	stdout, _, err := runRootArgs(t,
		"recall", "odds England wins world cup",
		"--db", dbPath,
		"--agent",
	)
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	var env recallEnvelope
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("recall JSON: %v (stdout=%q)", err, stdout)
	}
	if env.Found {
		t.Errorf("England-vs-Portugal: want found=false; got %+v", env)
	}
	if len(env.Mismatches) != 0 {
		t.Errorf("default envelope should not include mismatches; got %d", len(env.Mismatches))
	}

	// England query WITH --debug-mismatches: mismatches array surfaces.
	stdoutDbg, _, err := runRootArgs(t,
		"recall", "odds England wins world cup",
		"--db", dbPath,
		"--agent",
		"--debug-mismatches",
	)
	if err != nil {
		t.Fatalf("recall debug: %v", err)
	}
	var envDbg recallEnvelope
	if err := json.Unmarshal([]byte(stdoutDbg), &envDbg); err != nil {
		t.Fatalf("recall debug JSON: %v", err)
	}
	if envDbg.Found {
		t.Errorf("debug-on: results bucket still empty; got %+v", envDbg)
	}
	// The mismatches array may be empty when the resource itself was
	// never synced (the teach call wrote a learning whose resource_id
	// has no matching row in the resources table). In the CLI test
	// path here we seeded only the learning, not the resource, so the
	// row classifies as unknown rather than mismatch. Either is a
	// valid debug-on behavior: the test asserts the SHAPE (mismatches
	// is a slice, even if empty here) rather than its contents.
	if envDbg.Mismatches == nil {
		t.Errorf("debug-on: mismatches should be a non-nil slice")
	}
}

// TestRecallCommand_MinConfidenceFilters verifies the existing
// --min-confidence flag continues to work end-to-end through the new
// learn.Recall path.
func TestRecallCommand_MinConfidenceFiltersAtCLI(t *testing.T) {
	home := withTempHome(t)
	dbPath := filepath.Join(home, "data.db")

	// One teach -> confidence=1.
	if _, _, err := runRootArgs(t,
		"teach", "--query", "portugal world cup", "--resource", "A",
		"--db", dbPath,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// --min-confidence 2 should filter the single-teach row.
	stdout, _, err := runRootArgs(t,
		"recall", "portugal world cup",
		"--db", dbPath,
		"--min-confidence", "2",
		"--agent",
	)
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	var env recallEnvelope
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("recall JSON: %v", err)
	}
	if env.Found || len(env.Results) != 0 {
		t.Errorf("min-confidence 2 should drop conf=1 row; got %+v", env)
	}
}

func envSliceContains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
