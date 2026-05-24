// store_migration.go retrofits the learn-loop migrations into a CLI's
// internal/store/store.go. Two paths are supported:
//
//   - **Anchor mode.** Post-U6 generator output carries the literal
//     `// CLI Printing Press: learn migrations` marker right before the
//     learn-loop CREATE statements. The sweep finds the marker, replaces
//     the canonical migrations block (marker + 5 statements), and bumps
//     StoreSchemaVersion to the learn-enabled value.
//   - **Bootstrap mode.** Pre-U6 generator output (and every CLI
//     currently shipping in the published library) carries the
//     `migrations := []string{...}` slice but no anchor. The sweep
//     locates the slice via AST, picks an insertion point at the end of
//     the slice literal, and inserts the anchor + 5 CREATE statements +
//     a StoreSchemaVersion declaration so future re-sweeps land in the
//     anchor path. Refuses when the AST search finds multiple `migrations`
//     slices or no slice at all — those shapes are outside the contract
//     and surface as "manual review needed" skips.
//
// Idempotency: a second run on bootstrap-emitted output finds the anchor
// (because bootstrap wrote it) and runs the anchor path with zero diff.
// A second anchor-path run also produces zero diff: the block is
// re-emitted verbatim from the canonical source below.

package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strings"
)

const (
	learnMigrationAnchor = "// CLI Printing Press: learn migrations"
	learnSchemaVersion   = 3
)

// hasLearnMigrationAnchor reports whether store.go already carries the
// canonical learn-migrations marker. Used by sweepCLI to decide whether
// the anchor path or the bootstrap path runs for a given CLI.
func hasLearnMigrationAnchor(src []byte) bool {
	return strings.Contains(string(src), learnMigrationAnchor)
}

// canonicalLearnMigrationsBlock is the exact text the generator emits
// between the FTS create statement and the per-CLI tables (post-U6).
// Tab-indented to match the template's emission so the file remains
// gofmt-clean after the splice. Keep in sync with
// cli-printing-press/internal/generator/templates/store.go.tmpl.
const canonicalLearnMigrationsBlock = `		// CLI Printing Press: learn migrations
		` + "`CREATE TABLE IF NOT EXISTS search_learnings (\n" +
	`			query_pattern TEXT NOT NULL,
			query_entities TEXT NOT NULL DEFAULT '[]',
			resource_ids TEXT NOT NULL DEFAULT '[]',
			resource_type TEXT NOT NULL,
			venue TEXT,
			action TEXT,
			confidence INTEGER NOT NULL DEFAULT 0,
			source TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (query_pattern, resource_type)
		` + "`,\n" +
	"		`CREATE TABLE IF NOT EXISTS search_patterns (\n" +
	`			template TEXT NOT NULL,
			entity_kind TEXT NOT NULL,
			confidence INTEGER NOT NULL DEFAULT 0,
			source TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (template, entity_kind)
		` + "`,\n" +
	"		`CREATE TABLE IF NOT EXISTS entity_lookups (\n" +
	`			canonical TEXT NOT NULL,
			alias TEXT NOT NULL,
			kind TEXT NOT NULL,
			source TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			UNIQUE (canonical, alias, kind)
		` + "`,\n" +
	"		`CREATE TABLE IF NOT EXISTS teach_log_metadata (\n" +
	`			rotation_at DATETIME,
			last_size_bytes INTEGER NOT NULL DEFAULT 0
		` + "`,\n" +
	"		`CREATE VIRTUAL TABLE IF NOT EXISTS search_learnings_fts USING fts5(\n" +
	`			query_pattern, tokenize='porter unicode61'
		` + "`,"

// learnMigrationsBlockEndMarker is the closing fence of the canonical
// block. The first CREATE TABLE outside the block (per-CLI tables,
// emitted from spec.Tables) starts with this anchor pattern in the
// store template. Used to delimit the rewrite range.
const learnMigrationsBlockEndMarker = "`CREATE VIRTUAL TABLE IF NOT EXISTS search_learnings_fts USING fts5("

// patchStoreMigrations rewrites the learn-migrations block in store.go
// to its canonical content and bumps StoreSchemaVersion. When the
// anchor is missing it routes through bootstrapLearnMigrations, which
// locates the migrations slice and seeds the anchor + block in place.
// Returns the new source, a changed boolean, and any error encountered
// while locating the block boundaries.
func patchStoreMigrations(src string, _ sweepCtx) (string, bool, error) {
	if !strings.Contains(src, learnMigrationAnchor) {
		// Anchor missing — fall through to bootstrap mode. A successful
		// bootstrap leaves the file in anchor-mode shape so the next
		// sweep run hits the anchor branch and produces zero diff.
		return bootstrapLearnMigrations(src)
	}

	// Locate the block start (the anchor line) and end (the trailing
	// backtick of the search_learnings_fts CREATE).
	startIdx := strings.Index(src, learnMigrationAnchor)
	if startIdx < 0 {
		return src, false, fmt.Errorf("anchor missing after presence check")
	}
	// Walk back to the line start so the replacement begins at the
	// canonical indent.
	lineStart := startIdx
	for lineStart > 0 && src[lineStart-1] != '\n' {
		lineStart--
	}

	// Locate the search_learnings_fts CREATE that closes the block.
	ftsIdx := strings.Index(src[lineStart:], learnMigrationsBlockEndMarker)
	if ftsIdx < 0 {
		return src, false, fmt.Errorf("learn-migrations block end marker not found")
	}
	ftsIdx += lineStart
	// Walk forward to the closing backtick + comma of the FTS CREATE.
	rest := src[ftsIdx:]
	tickIdx := strings.Index(rest, "`,")
	if tickIdx < 0 {
		return src, false, fmt.Errorf("FTS create not terminated with backtick+comma")
	}
	blockEnd := ftsIdx + tickIdx + len("`,")
	// Include the trailing newline.
	if blockEnd < len(src) && src[blockEnd] == '\n' {
		blockEnd++
	}

	canonical := canonicalLearnMigrationsBlock + "\n"
	newSrc := src[:lineStart] + canonical + src[blockEnd:]

	// Bump StoreSchemaVersion to learnSchemaVersion if it's lower.
	newSrc = bumpStoreSchemaVersion(newSrc, learnSchemaVersion)

	// Older library CLIs don't expose Store.DB(); the sweep-emitted
	// teach.go calls it directly so we back-fill here. Idempotent.
	newSrc = ensureStoreDBAccessor(newSrc)

	return newSrc, newSrc != src, nil
}

// hasStoreDBAccessor reports whether the file already declares a
// `func (s *Store) DB() *sql.DB` method (or close variant). The
// sweep-emitted teach.go calls s.DB() to get the underlying
// *sql.DB; older published library CLIs predate this accessor and
// need it back-filled by ensureStoreDBAccessor.
func hasStoreDBAccessor(src string) bool {
	// Conservative: match the canonical generator emission shape. A
	// hand-written variant with a different return signature or
	// receiver name would be on the operator to keep working.
	return strings.Contains(src, ") DB() *sql.DB {") ||
		strings.Contains(src, ") DB() *sql.DB{")
}

// ensureStoreDBAccessor appends `func (s *Store) DB() *sql.DB` to
// store.go when missing. The accessor exposes the unexported `db`
// field so internal/cli/teach.go (sweep-emitted) can pass the
// underlying *sql.DB into the internal/learn package without
// reaching across the package boundary. Idempotent: a second pass
// detects the method via hasStoreDBAccessor and is a no-op.
//
// Refuses cleanly when the file does not declare a `*Store` receiver
// elsewhere — the file may use a different Store name (rare, but
// surfaceable as a refusal rather than a partial splice). The check
// looks for `func (s *Store) ` as a stable signal of the canonical
// shape.
func ensureStoreDBAccessor(src string) string {
	if hasStoreDBAccessor(src) {
		return src
	}
	if !strings.Contains(src, "func (s *Store) ") {
		// File doesn't use the canonical Store receiver name; leave
		// alone so a downstream compile error surfaces the mismatch
		// rather than the sweep emitting an out-of-shape accessor.
		return src
	}
	accessor := "\n// DB exposes the underlying *sql.DB for callers that need to run\n" +
		"// ad-hoc queries. Inserted by the sweep-learn-install retrofit;\n" +
		"// the canonical generator template defines an equivalent.\n" +
		"// Callers must not call Close on the returned handle.\n" +
		"func (s *Store) DB() *sql.DB {\n" +
		"\treturn s.db\n" +
		"}\n"
	out := src
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return out + accessor
}

// bootstrapLearnMigrations seeds the anchor + learn-migrations block
// into a pre-U6 store.go that carries the canonical `migrations`
// slice literal but no anchor. The slice is located via AST so a
// store.go variant whose migrations identifier was renamed or which
// has been hand-edited into a non-templated shape gets a clear refusal
// rather than a partial splice.
//
// Refusal conditions:
//
//   - The file does not parse as Go.
//   - The file has no `migrations := []string{...}` short-var-decl with
//     a slice literal initializer.
//   - The file has more than one such declaration (ambiguous splice
//     site).
//   - The slice literal's source-range cannot be resolved against the
//     in-memory source bytes.
//
// Successful bootstrap returns the original source with:
//
//   - The 6 learn-migrations entries (anchor comment + 5 CREATE
//     statements) inserted at the slice's tail, right before the
//     closing brace.
//   - StoreSchemaVersion declared (or bumped) to learnSchemaVersion.
func bootstrapLearnMigrations(src string) (string, bool, error) {
	loc, err := findMigrationsSliceRange(src)
	if err != nil {
		return src, false, err
	}

	// Determine the indent for the inserted lines by looking at the
	// last entry (or the opening brace if the slice is empty). The
	// canonical indent under `migrations := []string{` in the
	// generator template is two tabs.
	indent := detectSliceEntryIndent(src, loc)

	insertion := renderBootstrapLearnEntries(indent)

	// Find the last comma+newline (or `{` followed by newline) before
	// the closing brace and splice the new entries there. We insert
	// right before the closing brace so the block ends with our final
	// entry's trailing comma — matching the trailing-comma idiom Go
	// composite literals require.
	insertAt := loc.closeBrace
	// Walk back past the immediately preceding whitespace so the
	// closing brace stays on its own line and our insertion has its
	// own indent.
	for insertAt > 0 && (src[insertAt-1] == ' ' || src[insertAt-1] == '\t') {
		insertAt--
	}
	if insertAt > 0 && src[insertAt-1] == '\n' {
		// Already on its own line; insert before the indent of `}`.
	}

	newSrc := src[:insertAt] + insertion + src[insertAt:]

	// Defense in depth: assert the anchor + the FTS marker are now in
	// the output. A regression in renderBootstrapLearnEntries would
	// otherwise pass silently.
	if !strings.Contains(newSrc, learnMigrationAnchor) {
		return src, false, fmt.Errorf("bootstrap: anchor not present after insertion")
	}
	if !strings.Contains(newSrc, learnMigrationsBlockEndMarker) {
		return src, false, fmt.Errorf("bootstrap: FTS marker not present after insertion")
	}

	// Ensure StoreSchemaVersion is present and bumped. Pre-U6 store.go
	// may not declare it at all; ensureStoreSchemaVersion adds it
	// alongside the package declaration if missing.
	newSrc = ensureStoreSchemaVersion(newSrc, learnSchemaVersion)

	// Older library CLIs don't expose Store.DB(); the sweep-emitted
	// teach.go calls it directly so we back-fill here. Idempotent.
	newSrc = ensureStoreDBAccessor(newSrc)

	return newSrc, newSrc != src, nil
}

// migrationsSliceRange is the AST-derived source range of a
// `migrations := []string{...}` composite literal.
type migrationsSliceRange struct {
	openBrace  int // offset of `{`
	closeBrace int // offset of `}` (matching `{`)
}

// findMigrationsSliceRange locates the unique migrations slice literal
// in store.go. Refuses on ambiguous or absent shapes.
func findMigrationsSliceRange(src string) (migrationsSliceRange, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "store.go", src, parser.ParseComments)
	if err != nil {
		return migrationsSliceRange{}, fmt.Errorf("bootstrap: parse store.go: %w", err)
	}

	var matches []*ast.CompositeLit
	ast.Inspect(f, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		// Match `migrations := []string{...}` — short-var-decl with a
		// single LHS identifier named "migrations" and a single
		// composite-literal RHS whose element type is []string.
		if assign.Tok != token.DEFINE || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		ident, ok := assign.Lhs[0].(*ast.Ident)
		if !ok || ident.Name != "migrations" {
			return true
		}
		comp, ok := assign.Rhs[0].(*ast.CompositeLit)
		if !ok {
			return true
		}
		arr, ok := comp.Type.(*ast.ArrayType)
		if !ok {
			return true
		}
		elt, ok := arr.Elt.(*ast.Ident)
		if !ok || elt.Name != "string" {
			return true
		}
		matches = append(matches, comp)
		return true
	})

	if len(matches) == 0 {
		return migrationsSliceRange{}, fmt.Errorf("bootstrap: no `migrations := []string{...}` slice found in store.go (manual review needed)")
	}
	if len(matches) > 1 {
		return migrationsSliceRange{}, fmt.Errorf("bootstrap: multiple `migrations := []string{...}` slices found (manual review needed)")
	}

	comp := matches[0]
	open := fset.Position(comp.Lbrace).Offset
	close := fset.Position(comp.Rbrace).Offset
	if open < 0 || close <= open || close >= len(src) {
		return migrationsSliceRange{}, fmt.Errorf("bootstrap: cannot resolve slice brace offsets")
	}
	return migrationsSliceRange{openBrace: open, closeBrace: close}, nil
}

// detectSliceEntryIndent returns the leading whitespace used by entries
// in the migrations slice. Falls back to two tabs (the canonical
// generator-template indent) when the slice is empty or detection
// fails.
func detectSliceEntryIndent(src string, loc migrationsSliceRange) string {
	// Look at the first non-blank line after `{`.
	i := loc.openBrace + 1
	for i < loc.closeBrace && (src[i] == ' ' || src[i] == '\t' || src[i] == '\n' || src[i] == '\r') {
		i++
	}
	if i >= loc.closeBrace {
		return "\t\t"
	}
	// Walk back to the line start and collect the leading whitespace.
	lineStart := i
	for lineStart > 0 && src[lineStart-1] != '\n' {
		lineStart--
	}
	indent := ""
	for j := lineStart; j < i; j++ {
		if src[j] == ' ' || src[j] == '\t' {
			indent += string(src[j])
		} else {
			break
		}
	}
	if indent == "" {
		return "\t\t"
	}
	return indent
}

// renderBootstrapLearnEntries returns the canonical learn-migration
// block (anchor + 5 CREATE statements) reindented to indent. Each
// entry ends in ",\n". The output is intended to be spliced in front
// of the slice's closing `}`.
func renderBootstrapLearnEntries(indent string) string {
	// canonicalLearnMigrationsBlock is rendered at the canonical
	// "two-tab" indent (matching the generator template's depth inside
	// migrations := []string{ ... }). Reindent each line by replacing
	// the leading "\t\t" with the actual detected indent so a CLI
	// whose migrations slice happens to use spaces still lines up.
	const canonicalIndent = "\t\t"
	var b strings.Builder
	for _, line := range strings.Split(canonicalLearnMigrationsBlock, "\n") {
		if line == "" {
			b.WriteByte('\n')
			continue
		}
		if strings.HasPrefix(line, canonicalIndent) {
			b.WriteString(indent)
			b.WriteString(line[len(canonicalIndent):])
		} else {
			b.WriteString(line)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

var storeSchemaVersionRe = regexp.MustCompile(`const StoreSchemaVersion = (\d+)`)

// bumpStoreSchemaVersion replaces `const StoreSchemaVersion = N` with
// the target when N is lower; idempotent otherwise. Does not touch
// any other `const Store...` declarations.
func bumpStoreSchemaVersion(src string, target int) string {
	return storeSchemaVersionRe.ReplaceAllStringFunc(src, func(match string) string {
		sub := storeSchemaVersionRe.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		current := 0
		_, err := fmt.Sscanf(sub[1], "%d", &current)
		if err != nil {
			return match
		}
		if current >= target {
			return match
		}
		return fmt.Sprintf("const StoreSchemaVersion = %d", target)
	})
}

// ensureStoreSchemaVersion guarantees the file declares
// `const StoreSchemaVersion = target`. If a declaration is already
// present, falls through to bumpStoreSchemaVersion. Otherwise inserts
// a fresh `const StoreSchemaVersion = target` declaration right after
// the package's imports block (or right after the package declaration
// when no imports block exists) so bootstrap can run against pre-U6
// store.go files that never carried the constant. The const must
// land AFTER the import block — placing it between `package` and
// `import` produces `imports must appear before other declarations`.
func ensureStoreSchemaVersion(src string, target int) string {
	if storeSchemaVersionRe.MatchString(src) {
		return bumpStoreSchemaVersion(src, target)
	}
	// Use the Go parser to locate the end of the imports block (or
	// the end of the package keyword line when no imports exist).
	// A regex-based search is too brittle here: a comment containing
	// the word "package" or a string literal with `import (` would
	// trip up a string scan.
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "store.go", src, parser.ParseComments)
	if err != nil {
		return src
	}
	if f.Name == nil {
		return src
	}

	// Compute the byte offset just past the last import declaration's
	// closing paren / line. If there are no imports, use the offset
	// just past the package declaration's line.
	var insertAt int
	pkgLineEnd := fset.Position(f.Name.End()).Offset
	// Advance to the end of the package-name line.
	for pkgLineEnd < len(src) && src[pkgLineEnd] != '\n' {
		pkgLineEnd++
	}
	if pkgLineEnd < len(src) {
		pkgLineEnd++ // past the newline
	}
	insertAt = pkgLineEnd

	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.IMPORT {
			continue
		}
		end := fset.Position(gd.End()).Offset
		// Walk forward past the rest of the line so insertion does
		// not split a `)` from its trailing newline.
		for end < len(src) && src[end] != '\n' {
			end++
		}
		if end < len(src) {
			end++ // past the newline
		}
		if end > insertAt {
			insertAt = end
		}
	}

	// Skip past any blank line that follows so the const lands in
	// conventional position (one blank line after either the imports
	// block or the package declaration).
	hasBlankAfter := insertAt < len(src) && src[insertAt] == '\n'
	insertion := fmt.Sprintf("\n// StoreSchemaVersion is the on-disk schema version this binary understands.\nconst StoreSchemaVersion = %d\n", target)
	if hasBlankAfter {
		insertAt++
		insertion = fmt.Sprintf("// StoreSchemaVersion is the on-disk schema version this binary understands.\nconst StoreSchemaVersion = %d\n\n", target)
	}
	return src[:insertAt] + insertion + src[insertAt:]
}
