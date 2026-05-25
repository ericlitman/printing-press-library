package main

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// goldenLearnAPIDir is the path (relative to a developer checkout) to
// the cli-printing-press golden fixture for the generate-learn-loop-api
// case. The parity test resolves it by walking up from the sweep tool
// directory. When the checkout isn't a sibling, the test SKIPs rather
// than fails — the parity test is a developer-machine signal, not a CI
// blocker.
const goldenLearnAPIDir = "testdata/golden/expected/generate-learn-loop-api/learn-loop-example"

// candidateCLIPrintingPressPaths lists locations the test will probe
// looking for a usable cli-printing-press checkout. The first one
// returning a readable golden fixture wins.
func candidateCLIPrintingPressPaths() []string {
	home := os.Getenv("HOME")
	return []string{
		filepath.Join(home, "cli-printing-press"),
		filepath.Join(home, ".claude", "worktrees", "cli-printing-press"),
		// Common sibling layouts.
		"../../../cli-printing-press",
		"../../../../cli-printing-press",
	}
}

// TestRenderLearnPackage_ByteForByteParity asserts that the sweep tool
// emits every learn-package file byte-for-byte identical to what the
// generator's golden fixture carries. This is the contract that lets
// the sweep retrofit existing CLIs without drifting from fresh-print
// output.
//
// The golden fixture used:
//
//	cli-printing-press/testdata/golden/expected/
//	    generate-learn-loop-api/learn-loop-example/internal/learn/...
//
// Owner / Name / modulePath are extracted from the fixture's own
// header comment so the parity check stays accurate when those values
// change upstream.
func TestRenderLearnPackage_ByteForByteParity(t *testing.T) {
	goldenRoot := findGoldenLearnFixture(t)
	if goldenRoot == "" {
		t.Skip("cli-printing-press golden fixture not found; parity test is developer-only")
	}

	ctx := sweepCtx{
		// Mirror the generator's spec values for the
		// learn-loop-example fixture (printing-press-golden owner;
		// learn-loop-example api/name; learn-loop-example-pp-cli
		// module).
		CLIDir:     "/tmp/parity-target",
		CLIName:    "learn-loop-example-pp-cli",
		APIName:    "learn-loop-example",
		Category:   "other",
		OwnerName:  "printing-press-golden",
		ModulePath: "learn-loop-example-pp-cli",
	}
	emitted, err := renderLearnPackage(ctx)
	if err != nil {
		t.Fatalf("renderLearnPackage: %v", err)
	}

	// Only assert parity for files the golden fixture carries. The
	// learn-loop-example artifacts.txt only locks one file per
	// subpackage (recall.go, extract.go, seeds.go, apply.go) plus
	// store.go in internal/store/.
	parityFiles := []string{
		"internal/learn/recall.go",
		"internal/learn/entities/extract.go",
		"internal/learn/lookups/seeds.go",
		"internal/learn/patterns/apply.go",
	}
	for _, rel := range parityFiles {
		t.Run(rel, func(t *testing.T) {
			goldenPath := filepath.Join(goldenRoot, rel)
			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Skipf("golden file not present: %v", err)
			}
			got, ok := emitted[rel]
			if !ok {
				t.Fatalf("sweep did not emit %s; templates may not be embedded", rel)
			}
			// Normalize the copyright year so the test does not break
			// at midnight UTC every January 1st. The generator stamps
			// the fixture once, and the sweep stamps the current year
			// — both are correct by their own contract. The structural
			// parity (imports, identifiers, body) is what we want to
			// lock here.
			wantNormalized := stripCopyrightYear(string(want))
			gotNormalized := stripCopyrightYear(string(got))
			if wantNormalized != gotNormalized {
				t.Errorf("byte-for-byte parity mismatch for %s\n--- want ---\n%s\n--- got ---\n%s",
					rel, wantNormalized, gotNormalized)
			}
		})
	}
}

// TestEmittedTeachGo_NoExternalHelperDeps regression-pins Bug C from
// the U14 pilot sweep findings. The sweep emits teach.go into older
// library CLIs that do not declare the modern cli-printing-press
// baseline helpers (dryRunOK / printJSONFiltered /
// parentNoSubcommandRunE) and do not declare the context-aware store
// constructor (store.OpenWithContext). The sweep-emitted teach.go
// must therefore inline its equivalents under learn-prefixed names
// and use the plain store.Open constructor. Any direct reference to
// the modern helper names — and any reference to OpenWithContext —
// is a regression of the bug.
func TestEmittedTeachGo_NoExternalHelperDeps(t *testing.T) {
	ctx := sweepCtx{
		CLIName:    "demo-pp-cli",
		APIName:    "demo",
		Category:   "other",
		OwnerName:  "Tester",
		ModulePath: "github.com/example/demo-pp-cli",
	}
	emitted, err := renderLearnPackage(ctx)
	if err != nil {
		t.Fatalf("renderLearnPackage: %v", err)
	}
	src, ok := emitted["internal/cli/teach.go"]
	if !ok {
		t.Fatal("sweep did not emit internal/cli/teach.go")
	}
	gotStr := string(src)

	// Banned identifiers — any direct call into the modern-helpers
	// baseline that older library CLIs lack. The check intentionally
	// scans full lines (not just call sites) so a stray reference in
	// a comment also gets caught; the divergence note at the top of
	// teach.go.tmpl mentions these names but does not invoke them.
	bannedCallSitePatterns := []string{
		"dryRunOK(",
		"printJSONFiltered(",
		"parentNoSubcommandRunE(",
		"store.OpenWithContext(",
		// Three published library CLIs (airframe, monarch-money,
		// slack) ship without `func usageErr` — and two of those
		// three lack the underlying `cliError` type entirely — so
		// the sweep-emitted teach.go cannot rely on the canonical
		// generator helper. The inlined `learnUsageErr` wrapper
		// covers the gap; any direct `usageErr(...)` call site in
		// the emitted file is a regression toward the original Bug
		// C shape.
		"usageErr(",
	}
	for _, banned := range bannedCallSitePatterns {
		if strings.Contains(gotStr, banned) {
			t.Errorf("Bug C regression: sweep-emitted teach.go references %s (older library CLIs lack this helper)\n--- emitted ---\n%s",
				banned, gotStr)
		}
	}

	// Positive assertions: the inlined replacements must be present.
	mustContain := []string{
		"func learnDryRunOK(flags *rootFlags) bool",
		"func learnParentNoSubcommandRunE(flags *rootFlags)",
		"func learnPrintJSON(",
		"func learnUsageErr(",
		"store.Open(dbPath)",
	}
	for _, want := range mustContain {
		if !strings.Contains(gotStr, want) {
			t.Errorf("sweep-emitted teach.go missing %q (inlined helper expected)", want)
		}
	}
}

// TestEmittedLearnInitGo_NoExternalHelperDeps mirrors the teach.go
// guard for learn_init.go: the sweep-emitted runLearnInitOnce must
// not call into store.OpenWithContext.
func TestEmittedLearnInitGo_NoExternalHelperDeps(t *testing.T) {
	ctx := sweepCtx{
		CLIName:    "demo-pp-cli",
		APIName:    "demo",
		Category:   "other",
		OwnerName:  "Tester",
		ModulePath: "github.com/example/demo-pp-cli",
	}
	emitted, err := renderLearnPackage(ctx)
	if err != nil {
		t.Fatalf("renderLearnPackage: %v", err)
	}
	src, ok := emitted["internal/cli/learn_init.go"]
	if !ok {
		t.Fatal("sweep did not emit internal/cli/learn_init.go")
	}
	gotStr := string(src)
	if strings.Contains(gotStr, "store.OpenWithContext(") {
		t.Errorf("Bug C regression: sweep-emitted learn_init.go references store.OpenWithContext\n--- emitted ---\n%s", gotStr)
	}
	if !strings.Contains(gotStr, "store.Open(dbPath)") {
		t.Errorf("sweep-emitted learn_init.go missing store.Open(dbPath) call\n--- emitted ---\n%s", gotStr)
	}
}

// TestEmitsLearnInitGo_StubDefaults asserts the sweep's emission of
// internal/cli/learn_init.go matches the canonical "empty Learn block"
// shape: a no-op newLearnConfig (returns entities.NewConfig() with no
// RegisterTickerPattern / RegisterStopwords calls) and a no-op
// initLearn (returns nil, never touches the DB). The cli-printing-press
// golden fixture for learn-loop-example has populated seeds, so the
// parity target here is an embedded expected string rather than a
// golden file — documented in the embed comment below.
func TestEmitsLearnInitGo_StubDefaults(t *testing.T) {
	ctx := sweepCtx{
		CLIDir:     "/tmp/stub-target",
		CLIName:    "demo-pp-cli",
		APIName:    "demo",
		Category:   "other",
		OwnerName:  "Tester",
		ModulePath: "github.com/example/demo-pp-cli",
	}
	emitted, err := renderLearnPackage(ctx)
	if err != nil {
		t.Fatalf("renderLearnPackage: %v", err)
	}
	got, ok := emitted["internal/cli/learn_init.go"]
	if !ok {
		t.Fatal("sweep did not emit internal/cli/learn_init.go")
	}

	// Structural assertions: the stub shape avoids the imports and
	// call sites that only render under a populated Learn block.
	gotStr := string(got)
	mustContain := []string{
		"package cli",
		"func newLearnConfig() *entities.Config {",
		"cfg := entities.NewConfig()",
		"return cfg",
		"func initLearn(ctx context.Context, db *sql.DB) error {",
		"_ = ctx",
		"_ = db",
		"return nil",
		"var learnInitOnce sync.Once",
		"func runLearnInitOnce(ctx context.Context) {",
		"github.com/example/demo-pp-cli/internal/learn/entities",
		"github.com/example/demo-pp-cli/internal/store",
	}
	for _, want := range mustContain {
		if !strings.Contains(gotStr, want) {
			t.Errorf("stub learn_init.go missing %q\nrendered:\n%s", want, gotStr)
		}
	}

	mustNotContain := []string{
		// regexp import + RegisterTickerPattern only render when
		// Learn.TickerPatterns is non-empty.
		`"regexp"`,
		"RegisterTickerPattern",
		// RegisterStopwords only renders when Learn.Stopwords is
		// non-empty.
		"RegisterStopwords",
		// lookups import + SeedFromConfig only render when
		// Learn.EntityLookupSeeds is non-empty.
		"learn/lookups",
		"SeedFromConfig",
	}
	for _, unwanted := range mustNotContain {
		if strings.Contains(gotStr, unwanted) {
			t.Errorf("stub learn_init.go unexpectedly contains %q (likely a Learn-shape gate fired):\nrendered:\n%s", unwanted, gotStr)
		}
	}
}

// TestStubLearnInitGoIsBenignNoOp asserts the stub learn_init.go
// parses as syntactically valid Go. The "benign no-op" claim in the
// task statement turns on the package compiling against the rest of
// the printed CLI; this test guards the upstream parse step (the
// sweep produces something gofmt+parser will accept) without
// requiring a full module compile in the test harness.
func TestStubLearnInitGoIsBenignNoOp(t *testing.T) {
	ctx := sweepCtx{
		CLIDir:     "/tmp/stub-target",
		CLIName:    "demo-pp-cli",
		APIName:    "demo",
		Category:   "other",
		OwnerName:  "Tester",
		ModulePath: "github.com/example/demo-pp-cli",
	}
	emitted, err := renderLearnPackage(ctx)
	if err != nil {
		t.Fatalf("renderLearnPackage: %v", err)
	}
	src, ok := emitted["internal/cli/learn_init.go"]
	if !ok {
		t.Fatal("sweep did not emit internal/cli/learn_init.go")
	}

	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, "learn_init.go", src, parser.ParseComments); err != nil {
		t.Fatalf("stub learn_init.go does not parse: %v\n--- source ---\n%s", err, src)
	}
}

// stripCopyrightYear normalizes the year token in the
// `// Copyright YYYY ...` header line so a year tick doesn't break
// parity. Only the YYYY digit run gets replaced; the rest of the
// header is preserved.
func stripCopyrightYear(s string) string {
	const prefix = "// Copyright "
	idx := strings.Index(s, prefix)
	if idx != 0 {
		return s
	}
	rest := s[len(prefix):]
	// Walk past digits.
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	return prefix + "YYYY" + rest[end:]
}

// findGoldenLearnFixture probes a small number of likely locations for
// a cli-printing-press checkout's golden learn fixture. Returns the
// path to the learn-loop-example directory if one is found, or ""
// otherwise.
func findGoldenLearnFixture(t *testing.T) string {
	t.Helper()
	for _, base := range candidateCLIPrintingPressPaths() {
		candidate := filepath.Join(base, goldenLearnAPIDir)
		if _, err := os.Stat(filepath.Join(candidate, "internal", "learn", "recall.go")); err == nil {
			return candidate
		}
	}
	return ""
}

// TestRenderLearnPackage_AllFilesPresent verifies the sweep emits the
// complete file set the generator emits for a learn-enabled spec:
// the internal/learn data package plus the internal/cli teach.go +
// learn_init.go cobra surface. A new template added to the generator
// but missed in the sweep would surface as a smaller emission count.
func TestRenderLearnPackage_AllFilesPresent(t *testing.T) {
	ctx := sweepCtx{
		CLIName:    "test-pp-cli",
		APIName:    "test",
		Category:   "other",
		OwnerName:  "Tester",
		ModulePath: "github.com/example/test-pp-cli",
	}
	emitted, err := renderLearnPackage(ctx)
	if err != nil {
		t.Fatalf("renderLearnPackage: %v", err)
	}
	expectedFiles := []string{
		"internal/learn/doc.go",
		"internal/learn/normalize.go",
		"internal/learn/match.go",
		"internal/learn/recall.go",
		"internal/learn/teach.go",
		"internal/learn/teach_log.go",
		"internal/learn/preseed.go",
		"internal/learn/entities/config.go",
		"internal/learn/entities/extract.go",
		"internal/learn/lookups/store.go",
		"internal/learn/lookups/seeds.go",
		"internal/learn/patterns/doc.go",
		"internal/learn/patterns/store.go",
		"internal/learn/patterns/extract.go",
		"internal/learn/patterns/apply.go",
		// Cobra-surface files emitted alongside the learn data package.
		"internal/cli/teach.go",
		"internal/cli/learn_init.go",
	}
	for _, f := range expectedFiles {
		if _, ok := emitted[f]; !ok {
			t.Errorf("expected emitted file %s missing from sweep output", f)
		}
	}
	if testing.Verbose() {
		t.Logf("emitted %d files on %s/%s", len(emitted), runtime.GOOS, runtime.GOARCH)
	}
}

// TestEmitsLearnRootShim_OnlyForFactoryShape asserts the rootFlags
// compatibility shim is emitted IFF ctx.RootShape == rootShapeFactory.
// Canonical-shape CLIs already declare a rootFlags struct in their own
// root.go; emitting the shim there would cause a duplicate-type
// compile error.
func TestEmitsLearnRootShim_OnlyForFactoryShape(t *testing.T) {
	baseCtx := sweepCtx{
		CLIName:    "demo-pp-cli",
		APIName:    "demo",
		Category:   "other",
		OwnerName:  "Tester",
		ModulePath: "github.com/example/demo-pp-cli",
	}

	t.Run("factory-shape-emits-shim", func(t *testing.T) {
		ctx := baseCtx
		ctx.RootShape = rootShapeFactory
		emitted, err := renderLearnPackage(ctx)
		if err != nil {
			t.Fatalf("renderLearnPackage: %v", err)
		}
		shim, ok := emitted["internal/cli/learn_root_shim.go"]
		if !ok {
			t.Fatal("expected factory-shape emission to include learn_root_shim.go")
		}
		// Structural assertions: the shim must declare the rootFlags
		// struct with the three fields teach.go references.
		mustContain := []string{
			"package cli",
			"type rootFlags struct {",
			"noLearn bool",
			"dryRun bool",
			"asJSON bool",
		}
		for _, want := range mustContain {
			if !strings.Contains(string(shim), want) {
				t.Errorf("shim missing %q\n--- shim ---\n%s", want, shim)
			}
		}
	})

	t.Run("canonical-shape-skips-shim", func(t *testing.T) {
		ctx := baseCtx
		ctx.RootShape = rootShapeFlagsStruct
		emitted, err := renderLearnPackage(ctx)
		if err != nil {
			t.Fatalf("renderLearnPackage: %v", err)
		}
		if _, ok := emitted["internal/cli/learn_root_shim.go"]; ok {
			t.Error("canonical-shape emission must NOT include learn_root_shim.go (would clash with host rootFlags)")
		}
	})

	t.Run("unknown-shape-skips-shim", func(t *testing.T) {
		// Zero-value RootShape (rootShapeUnknown) must be treated as
		// "no shim" so direct callers that don't thread the shape
		// (older tests, future call sites) default to the safe path.
		ctx := baseCtx
		emitted, err := renderLearnPackage(ctx)
		if err != nil {
			t.Fatalf("renderLearnPackage: %v", err)
		}
		if _, ok := emitted["internal/cli/learn_root_shim.go"]; ok {
			t.Error("unknown-shape emission must NOT include learn_root_shim.go by default")
		}
	})
}

// TestRenderLearnPackage_Idempotent runs the renderer twice and
// asserts identical output. The renderer reads embedded templates and
// runs gofmt, both of which are pure; this test guards against a
// future change introducing non-determinism (e.g., a map iteration
// landing in a template).
func TestRenderLearnPackage_Idempotent(t *testing.T) {
	ctx := sweepCtx{
		CLIName:    "idem-pp-cli",
		APIName:    "idem",
		Category:   "other",
		OwnerName:  "Tester",
		ModulePath: "github.com/example/idem-pp-cli",
	}
	first, err := renderLearnPackage(ctx)
	if err != nil {
		t.Fatalf("first render: %v", err)
	}
	second, err := renderLearnPackage(ctx)
	if err != nil {
		t.Fatalf("second render: %v", err)
	}
	if len(first) != len(second) {
		t.Fatalf("file count differs between runs: %d vs %d", len(first), len(second))
	}
	for rel, content := range first {
		other, ok := second[rel]
		if !ok {
			t.Errorf("file %s emitted in first run but not second", rel)
			continue
		}
		if string(content) != string(other) {
			t.Errorf("file %s differs between runs", rel)
		}
	}
}
