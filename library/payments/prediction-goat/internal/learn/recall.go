// Copyright 2026 mvanhorn. Licensed under Apache-2.0. See LICENSE.

// recall.go is the entity-aware read path for the learning subsystem.
// `Recall(ctx, db, query, opts)` walks the search_learnings table,
// scores each candidate against the query by token-set Jaccard on the
// non-entity normalized form, classifies each candidate by the entity
// match validator (see match.go), and returns:
//
//   - results: entity_match in {exact, partial, unknown}, sorted
//     exact > partial > unknown, then confidence DESC, then
//     last_observed_at DESC.
//   - mismatches: entity_match == mismatch rows that cleared the
//     Jaccard threshold. Included in the envelope only when
//     opts.DebugMismatches is true so the LLM can see why a
//     high-Jaccard candidate was filtered, without polluting the
//     default path with noise.
//
// Per-hit warnings come from validateResource: parent-event-vs-child,
// resource-not-in-store, low-confidence. Top-level warnings come from
// classifyTopLevel: e.g., "no learnings found for this query family".
//
// Why a package separate from internal/store: the U3 plan calls for
// the learning subsystem to be liftable into a generator template
// without dragging prediction-goat-specific schema, sync, or topic
// code with it. internal/learn/ depends on internal/store/ via a
// narrow handle (*sql.DB plus the SQL strings it issues), not on the
// Store wrapper. Per the U3 section of
// docs/plans/2026-05-23-002-feat-prediction-goat-smart-learning-plan.md.

package learn

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mvanhorn/printing-press-library/library/payments/prediction-goat/internal/learn/recipes"
)

// jsonUnmarshal aliases json.Unmarshal so callers in this file can
// stay tightly grouped with the SQL-and-JSON shape they use. Keeps
// the import compact at the top.
var jsonUnmarshal = json.Unmarshal

// Default thresholds. Keep in sync with the documented contract in
// SKILL.md and the legacy internal/store.Recall implementation; the
// recall match-score floor is 0.6 (token-set Jaccard) and the default
// result cap is 10.
const (
	defaultJaccardMin  = 0.6
	defaultRecallLimit = 10
	defaultMinConfidence = 1
)

// Source values written into Hit.Source for the recall envelope.
// "taught" / "inferred-*" come from search_learnings rows directly.
// "recipe" marks a hit produced by the U10 generalization layer
// (recipes.Apply), distinguished from a direct learning so the
// agent can tell the two apart in {found, results}.
const (
	// SourceRecipe is the marker for a Hit synthesized by the recipe
	// substitution engine rather than read from search_learnings.
	SourceRecipe = "recipe"
)

// LearningActionBoost mirrors the action string stored in
// search_learnings for a default rerank rule. Re-exported here as
// a const so recall.go can stamp synthetic recipe hits with the
// same action shape without importing the store package (avoids an
// import-cycle in the lower layers).
const LearningActionBoost = "boost"

// Hit is one row in the recall envelope. Field tags mirror the JSON
// contract the LLM reads; do not rename without updating SKILL.md
// (U8) and the agent-context schema (U7).
type Hit struct {
	ResourceID       string     `json:"resource_id"`
	ResourceType     string     `json:"resource_type,omitempty"`
	Venue            string     `json:"venue,omitempty"`
	Action           string     `json:"action"`
	Confidence       int        `json:"confidence"`
	MatchScore       float64    `json:"match_score"`
	EntityMatch      string     `json:"entity_match"`
	ResourceEntities []string   `json:"resource_entities,omitempty"`
	Source           string     `json:"source"`
	LastObservedAt   *time.Time `json:"last_observed_at,omitempty"`
	AliasTarget      string     `json:"alias_target,omitempty"`
	Warnings         []string   `json:"warnings,omitempty"`
}

// Result is the top-level recall envelope. Found mirrors the legacy
// {found, results} shape. New fields (mismatches, normalized,
// query_entities, warnings) are additive; older agent prompts that
// only consume {found, results} continue to work.
type Result struct {
	Query         string   `json:"query"`
	Normalized    string   `json:"normalized"`
	QueryEntities []string `json:"query_entities"`
	Found         bool     `json:"found"`
	MatchScore    float64  `json:"match_score,omitempty"`
	Results       []Hit    `json:"results"`
	Mismatches    []Hit    `json:"mismatches,omitempty"`
	Warnings      []string `json:"warnings,omitempty"`
}

// Opts tunes Recall behavior. Defaults applied when zero:
//
//	MinConfidence -> 1 (any row)
//	Limit         -> 10
//	JaccardMin    -> 0.6
//
// DebugMismatches surfaces the mismatches array in the envelope.
// NoLearn short-circuits to an empty envelope (the LLM is in a
// deterministic flow that doesn't want learning state to affect
// results).
type Opts struct {
	MinConfidence   int
	Limit           int
	JaccardMin      float64
	DebugMismatches bool
	NoLearn         bool
}

// Recall is the entity-aware read path. db is the open *sql.DB
// pointing at the prediction-goat SQLite store (post-v4 migration;
// the search_learnings.query_entities column is required).
//
// Returns a non-nil Result on every call: even cold queries get the
// envelope shape populated with the normalized form and the query
// entities so the LLM can see what the CLI thinks it's matching.
// Errors are reserved for DB-level failures (SQL errors, scan
// errors); a query with zero candidates is success-empty.
//
// Sort order for results:
//
//  1. entity_match priority: exact > partial > unknown
//     (mismatch never reaches results; it's filtered to mismatches)
//  2. confidence DESC
//  3. match_score DESC
//  4. last_observed_at DESC (newer wins ties)
func Recall(ctx context.Context, db *sql.DB, query string, opts Opts) (Result, error) {
	normalized := Normalize(query, DefaultPredictionGoatConfig())
	result := Result{
		Query:         query,
		Normalized:    normalized.NonEntityNormalized,
		QueryEntities: append([]string(nil), normalized.Entities...),
		Results:       []Hit{},
	}
	if result.QueryEntities == nil {
		// Stable JSON: prefer [] over null for empty.
		result.QueryEntities = []string{}
	}
	if opts.NoLearn {
		// Disabled — leave envelope empty (Found=false, Results=[]).
		return result, nil
	}

	minConf := opts.MinConfidence
	if minConf <= 0 {
		minConf = defaultMinConfidence
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = defaultRecallLimit
	}
	jMin := opts.JaccardMin
	if jMin == 0 {
		jMin = defaultJaccardMin
	}
	if jMin < 0 {
		jMin = 0
	}

	// Build a per-call canonical resolver. Looks up each entity in
	// entity_lookups to find the canonical(s) it belongs to. Caches
	// per-call so a query with N entities does N lookups max, not
	// N*M where M is the number of candidate rows.
	resolver := NewCanonicalResolver(ctx, db)

	// Post-Normalize entity promotion via entity_lookups. The
	// capitalization-based entity extractor misses aliases like
	// "usa" (lowercase) or "49ers" (numeric prefix) because they
	// don't match any of its detection rules. PromoteEntities walks
	// the non-entity tokens and promotes any whose lowercased form
	// has a row in entity_lookups. Same helper runs at teach time so
	// stored query_entities stays symmetric with what recall sees.
	normalized = PromoteEntities(normalized, resolver)
	// Re-populate the envelope-facing slice / normalized string so a
	// caller reading the envelope sees the post-promotion shape.
	result.QueryEntities = append([]string(nil), normalized.Entities...)
	if result.QueryEntities == nil {
		result.QueryEntities = []string{}
	}
	result.Normalized = normalized.NonEntityNormalized

	// Build the query-side token set for Jaccard comparison. The
	// search_learnings.query_pattern column stores the non-entity
	// normalized form, so we compare token-set against that.
	queryTokens := strings.Fields(normalized.NonEntityNormalized)
	// A query with no non-entity tokens AND no entities can't match
	// anything; short-circuit before issuing SQL.
	if len(queryTokens) == 0 && len(normalized.Entities) == 0 && len(normalized.Tickers) == 0 {
		return result, nil
	}

	rows, err := db.QueryContext(ctx, `SELECT id, query_pattern, COALESCE(query_entities, ''),
		COALESCE(venue, ''), COALESCE(resource_type, ''), resource_id, action,
		COALESCE(alias_target, ''), source, confidence, created_at, last_observed_at, COALESCE(notes, '')
		FROM search_learnings
		WHERE confidence >= ?`, minConf)
	if err != nil {
		return result, fmt.Errorf("recall query: %w", err)
	}
	defer rows.Close()

	// Two buckets: results (exact/partial/unknown) and mismatches
	// (mismatch only). Collect both regardless of DebugMismatches so
	// the entity-match counts come out right; we filter mismatches
	// from the envelope at emit time if DebugMismatches is false.
	var hits []Hit
	var mismatches []Hit

	for rows.Next() {
		var (
			id              int64
			queryPattern    string
			storedEntities  string
			venue           string
			resourceType    string
			resourceID      string
			action          string
			aliasTarget     string
			source          string
			confidence      int
			createdAt       time.Time
			lastObserved    sql.NullTime
			notes           string
		)
		if err := rows.Scan(&id, &queryPattern, &storedEntities, &venue, &resourceType,
			&resourceID, &action, &aliasTarget, &source, &confidence, &createdAt, &lastObserved, &notes); err != nil {
			return result, fmt.Errorf("recall scan: %w", err)
		}

		// Score by token-set Jaccard against the stored row's
		// non-entity normalized form. U2 added query_entities but
		// preserved the legacy query_pattern shape (lowercase +
		// stopwords stripped via the old store.NormalizeQuery), which
		// still contains entity tokens like "portugal" lowered alongside
		// non-entity tokens like "cup" / "world". Re-running the new
		// entity-preserving Normalize over the stored pattern gives us
		// the symmetric NonEntityNormalized form to compare against.
		// This keeps U3 contained: no need to backfill query_pattern in
		// place, the read path normalizes on demand. Tradeoff: O(rows)
		// regex work per recall, but search_learnings is tiny per user.
		storedNorm := Normalize(queryPattern, DefaultPredictionGoatConfig())

		storedEntitySlice, _ := ParseStoredEntities(storedEntities)
		// If the stored query_entities column was populated by the
		// v3->v4 migration backfill but the live row was written by an
		// older binary that left the column NULL, fall back to running
		// the extractor over query_pattern now. The migration covers
		// the common path; this fallback covers the race where a
		// pre-v4 binary wrote a row after Open completed migration.
		if len(storedEntitySlice) == 0 {
			storedEntitySlice = storedNorm.Entities
		}
		// Opportunistic backfill for legacy null-entity rows. Rows
		// written by an older binary (or callers that bypass
		// PromoteEntities) have query_entities=NULL and
		// storedNorm.Entities=empty because query_pattern is
		// lowercased on write and the capitalization-based extractor
		// can't recover an entity in "usa". Walk the lowercased
		// query_pattern tokens through the resolver and use any that
		// resolve as the effective entity slice for THIS call.
		// Read-only -- the stored column stays NULL so we never
		// silently rewrite user data.
		if len(storedEntitySlice) == 0 {
			for _, tok := range strings.Fields(strings.ToLower(queryPattern)) {
				if cans := resolver.Resolve(tok); len(cans) > 0 {
					storedEntitySlice = append(storedEntitySlice, tok)
				}
			}
		}

		// Compute the stored non-entity tokens. We start from
		// storedNorm.NonEntityNormalized and additionally strip any
		// stored entity that the canonical resolver also recognizes
		// -- those are the entities the live query's PromoteEntities
		// just pulled out of queryTokens, so keeping them on the
		// stored side would make the Jaccard surface asymmetric. We
		// do NOT strip capitalization-only stored entities (e.g.
		// "World" / "Cup" mid-sentence at teach time) because at
		// recall time those same tokens stay as plain content tokens
		// in the lowercased live query and need to count on both
		// sides for the Jaccard ratio to stay accurate.
		storedNonEntityTokens := strings.Fields(storedNorm.NonEntityNormalized)
		if len(storedEntitySlice) > 0 {
			storedEntitySet := make(map[string]struct{}, len(storedEntitySlice))
			for _, e := range storedEntitySlice {
				key := strings.ToLower(strings.TrimSpace(e))
				if key == "" {
					continue
				}
				if cans := resolver.Resolve(key); len(cans) > 0 {
					storedEntitySet[key] = struct{}{}
				}
			}
			if len(storedEntitySet) > 0 {
				filtered := storedNonEntityTokens[:0]
				for _, tok := range storedNonEntityTokens {
					if _, isEntity := storedEntitySet[strings.ToLower(tok)]; isEntity {
						continue
					}
					filtered = append(filtered, tok)
				}
				storedNonEntityTokens = filtered
			}
		}
		score := Jaccard(queryTokens, storedNonEntityTokens)
		if score < jMin {
			continue
		}

		hit := Hit{
			ResourceID:   resourceID,
			ResourceType: resourceType,
			Venue:        venue,
			Action:       action,
			Confidence:   confidence,
			MatchScore:   score,
			Source:       source,
			AliasTarget:  aliasTarget,
		}
		if lastObserved.Valid {
			t := lastObserved.Time
			hit.LastObservedAt = &t
		}

		// Validate the resource side: pull its entities from the
		// resources table by (resource_type, resource_id), classify the
		// entity match, attach warnings.
		validateResource(ctx, db, &hit, normalized.Entities, storedEntitySlice)

		if hit.EntityMatch == EntityMatchMismatch {
			mismatches = append(mismatches, hit)
		} else {
			hits = append(hits, hit)
		}
	}
	if err := rows.Err(); err != nil {
		return result, fmt.Errorf("recall rows: %w", err)
	}

	// Generalization layer: after the direct-lookup pass, ask the
	// recipe engine whether any template applies to this query.
	// Recipe hits are merged into the results list with
	// Source="recipe" so the agent can distinguish them from direct
	// teaches. A direct hit on the same resource_id always wins
	// (the dedup step below skips a recipe hit that another row
	// already produced); when the direct pass found nothing AND a
	// recipe substitution does resolve, recall flips Found=true.
	//
	// Errors from Apply are swallowed at this seam: a malformed
	// recipe row or a lookup table miss isn't worth failing the
	// whole recall call over. The direct path's results are still
	// valid; the recipe layer is additive.
	recipeHits, _ := recipes.Apply(ctx, db, query, normalized.NonEntityNormalized, normalized.Entities, recipes.Opts{
		JaccardMin: jMin,
		Limit:      limit,
	})
	if len(recipeHits) > 0 {
		existing := make(map[string]struct{}, len(hits))
		for _, h := range hits {
			existing[hitKey(h.ResourceType, h.ResourceID)] = struct{}{}
		}
		for _, rh := range recipeHits {
			key := hitKey(rh.ResourceType, rh.ResourceID)
			if _, dup := existing[key]; dup {
				continue
			}
			existing[key] = struct{}{}
			hits = append(hits, Hit{
				ResourceID:       rh.ResourceID,
				ResourceType:     rh.ResourceType,
				Venue:            rh.Venue,
				Action:           LearningActionBoost,
				Confidence:       rh.Confidence,
				MatchScore:       rh.MatchScore,
				EntityMatch:      EntityMatchExact,
				ResourceEntities: rh.ResourceEntities,
				Source:           SourceRecipe,
				LastObservedAt:   rh.LastObservedAt,
			})
		}
	}

	sortHits(hits)
	sortHits(mismatches)

	if len(hits) > limit {
		hits = hits[:limit]
	}
	if len(mismatches) > limit {
		mismatches = mismatches[:limit]
	}

	// Stable JSON: nil-slice -> "null". Force [] when we have no hits
	// so the LLM's parser sees a consistent shape.
	if hits == nil {
		result.Results = []Hit{}
	} else {
		result.Results = hits
	}
	if opts.DebugMismatches {
		if mismatches == nil {
			result.Mismatches = []Hit{}
		} else {
			result.Mismatches = mismatches
		}
	}
	result.Found = len(hits) > 0
	if result.Found {
		result.MatchScore = hits[0].MatchScore
	}

	// Top-level warnings: when the query had non-empty extracted
	// content but no candidates cleared the Jaccard threshold (i.e.,
	// the whole search_learnings table had nothing to say), emit a
	// distinguishable signal so the LLM doesn't conflate "no
	// learnings" with "table is empty."
	if !result.Found && len(mismatches) == 0 {
		// The candidate set was empty above the threshold. Surface
		// only when query had real content to match against — empty-
		// query case is handled by the early return above.
		result.Warnings = append(result.Warnings, TopWarningNoLearningsForQueryFamily)
	}

	return result, nil
}

// validateResource fetches the resource the learning points at,
// extracts its entities, and updates the hit's EntityMatch +
// Warnings fields. Mutates the hit in place because the
// classification is intrinsic to the row, not a separate computation
// the caller might want to skip.
//
// storedEntitySlice is the query_entities JSON the migration / write
// path persisted on the learning row. It's a hint about what the
// teach call's query carried, NOT what the resource carries; the
// match validator runs against the freshly-extracted resource
// entities and uses storedEntitySlice as a fallback when the
// resource is missing from the store (so a still-valid teach with a
// since-pruned resource still classifies coherently).
func validateResource(ctx context.Context, db *sql.DB, hit *Hit, queryEntities, storedEntitySlice []string) {
	// Look up the resource. A miss isn't an error here — the resource
	// may have been pruned, never synced, or live in a resource_type
	// we don't have a fields-extractor for. We classify as unknown
	// and let the LLM decide whether to direct-fetch.
	var data string
	err := db.QueryRowContext(ctx,
		`SELECT data FROM resources WHERE resource_type = ? AND id = ?`,
		hit.ResourceType, hit.ResourceID,
	).Scan(&data)
	if err != nil {
		// Either the resource isn't in the store, or resource_type is
		// empty (older taught row from before resource_type was
		// recommended). Classify based on what we have stored from the
		// teach call's query_entities; if even that is empty, mark
		// unknown. A teach call that captured the query entities at
		// write time still carries enough signal for partial vs.
		// mismatch — that prevents an England query from matching a
		// Portugal teach even when the resource is gone.
		hit.Warnings = append(hit.Warnings, WarningResourceNotInStore)
		hit.EntityMatch = ClassifyEntityMatch(queryEntities, storedEntitySlice)
		if hit.EntityMatch == EntityMatchPartial && len(queryEntities) == 0 && len(storedEntitySlice) == 0 {
			// No signal on either side. The match is purely
			// Jaccard-driven; mark unknown so the LLM treats it as a
			// candidate, not a confirmed hit.
			hit.EntityMatch = EntityMatchUnknown
		}
		addLowConfidenceWarning(hit)
		return
	}

	resourceEntities := ResourceEntities(hit.ResourceType, []byte(data))
	hit.ResourceEntities = resourceEntities
	hit.EntityMatch = ClassifyEntityMatch(queryEntities, resourceEntities)

	// Parent-event guard. When the resource is a Kalshi event-level
	// ticker AND the query carries an entity, look for a child market
	// whose yes_sub_title or ticker matches the query entity. If a
	// child exists, the parent IS a relevant hit for this entity --
	// promote classification to exact (the parent can answer the query
	// transitively by enumerating children) AND emit the warning
	// naming the better child target so the LLM can fetch the child
	// directly instead of walking the parent.
	//
	// Why promote AND warn: a parent event whose title is generic
	// ("2026 Men's World Cup Winner") doesn't carry the query entity
	// in its own fields, so the direct ClassifyEntityMatch call above
	// will return mismatch. But the parent IS the right semantic
	// answer when a child exists; filtering it would hide a
	// well-formed hit. The warning is the actionable hint.
	if hit.ResourceType == "kalshi_events" && len(queryEntities) > 0 && IsKalshiParentTicker(hit.ResourceID) {
		if child := findKalshiChildForEntity(ctx, db, hit.ResourceID, queryEntities); child != "" {
			hit.Warnings = append(hit.Warnings, fmt.Sprintf("%s:%s", WarningParentEventWhenChildExists, child))
			if hit.EntityMatch == EntityMatchMismatch {
				hit.EntityMatch = EntityMatchExact
			}
		}
	}
	addLowConfidenceWarning(hit)
}

// addLowConfidenceWarning attaches the low-confidence flag when the
// hit hasn't cleared the documented skip threshold (>= 2). U4 will
// bump first-teach confidence so this warning becomes uncommon; for
// now it's the signal that tells the LLM "this is a single teach,
// not a re-confirmation — verify before skipping discovery."
func addLowConfidenceWarning(hit *Hit) {
	if hit.Confidence < 2 {
		hit.Warnings = append(hit.Warnings, WarningLowConfidence)
	}
}

// findKalshiChildForEntity looks for a child market under the given
// parent event whose yes_sub_title or ticker contains any of the
// query entities. Returns the child ticker, or "" if no match.
//
// Kalshi child markets are stored in the generic resources table
// (resource_type='kalshi_markets') with a JSON payload that carries
// event_ticker, ticker, and yes_sub_title fields. Rather than reach
// for a flat-column schema this CLI doesn't have, we scan the
// resource_type='kalshi_markets' subset and parse the JSON inline.
// The subset is small enough per parent (typically tens to a few
// hundred rows) that the full scan is the right tradeoff against
// adding a denormalized index.
func findKalshiChildForEntity(ctx context.Context, db *sql.DB, parentTicker string, queryEntities []string) string {
	if parentTicker == "" || len(queryEntities) == 0 {
		return ""
	}
	rows, err := db.QueryContext(ctx,
		`SELECT id, data FROM resources WHERE resource_type = 'kalshi_markets'`,
	)
	if err != nil {
		return ""
	}
	defer rows.Close()

	type childRow struct {
		id       string
		subtitle string
		ticker   string
	}
	var candidates []childRow
	for rows.Next() {
		var id, data string
		if err := rows.Scan(&id, &data); err != nil {
			return ""
		}
		var obj map[string]interface{}
		if err := jsonUnmarshal([]byte(data), &obj); err != nil {
			continue
		}
		// Only count rows whose event_ticker matches the parent.
		if et, _ := obj["event_ticker"].(string); et != parentTicker {
			continue
		}
		subtitle, _ := obj["yes_sub_title"].(string)
		ticker, _ := obj["ticker"].(string)
		if ticker == "" {
			ticker = id
		}
		candidates = append(candidates, childRow{id: id, subtitle: subtitle, ticker: ticker})
	}
	if err := rows.Err(); err != nil {
		return ""
	}

	for _, q := range queryEntities {
		ql := strings.ToLower(strings.TrimSpace(q))
		if ql == "" {
			continue
		}
		for _, c := range candidates {
			if c.subtitle != "" && strings.Contains(strings.ToLower(c.subtitle), ql) {
				return c.ticker
			}
			if c.ticker != "" {
				lt := strings.ToLower(c.ticker)
				if strings.Contains(lt, ql) {
					return c.ticker
				}
			}
		}
	}
	return ""
}

// sortHits orders hits per the U3 + U10 ranking contract:
//
//  1. entity_match priority: exact > partial > unknown > mismatch
//  2. within an entity_match tier, direct hits (non-recipe) outrank
//     recipe hits. The recipe layer's substitution-based "exact"
//     binding is still a real exact match, but a row a user explicitly
//     taught is a stronger signal than one the engine inferred via
//     generalization, so a direct exact wins ties.
//  3. confidence DESC
//  4. match_score DESC
//  5. last_observed_at DESC (newer wins ties)
func sortHits(hits []Hit) {
	sort.SliceStable(hits, func(i, j int) bool {
		pi := entityMatchPriority(hits[i].EntityMatch)
		pj := entityMatchPriority(hits[j].EntityMatch)
		if pi != pj {
			return pi < pj
		}
		// Direct teaches outrank recipe-synthesized hits within the
		// same entity_match tier. sourcePriority returns 0 for
		// direct hits, 1 for recipe hits.
		si := sourcePriority(hits[i].Source)
		sj := sourcePriority(hits[j].Source)
		if si != sj {
			return si < sj
		}
		if hits[i].Confidence != hits[j].Confidence {
			return hits[i].Confidence > hits[j].Confidence
		}
		if hits[i].MatchScore != hits[j].MatchScore {
			return hits[i].MatchScore > hits[j].MatchScore
		}
		ai := time.Time{}
		aj := time.Time{}
		if hits[i].LastObservedAt != nil {
			ai = *hits[i].LastObservedAt
		}
		if hits[j].LastObservedAt != nil {
			aj = *hits[j].LastObservedAt
		}
		return ai.After(aj)
	})
}

// sourcePriority returns the rank-key for a Hit's source. Direct
// teaches (taught / inferred-*) are 0; recipe-synthesized hits are
// 1. Unknown / future sources sort with direct teaches by default;
// a regression that ships a new source string without updating this
// table fails open rather than silently demoting unrelated hits.
func sourcePriority(source string) int {
	if source == SourceRecipe {
		return 1
	}
	return 0
}

// hitKey is the stable string key used to dedupe direct and recipe
// hits that point at the same resource. Empty resource_type matches
// anything in the comparison; some pre-U3 teaches don't carry a
// type but the resource_id is still unique per row.
func hitKey(resourceType, resourceID string) string {
	return resourceType + "|" + resourceID
}

// entityMatchPriority returns a stable sort key for entity-match
// values. Lower is better (sorts first). Unknown values get the
// worst priority so a row with a malformed entity_match doesn't
// silently outrank exact matches.
func entityMatchPriority(em string) int {
	switch em {
	case EntityMatchExact:
		return 0
	case EntityMatchPartial:
		return 1
	case EntityMatchUnknown:
		return 2
	case EntityMatchMismatch:
		return 3
	default:
		return 4
	}
}

// CanonicalResolver looks up entities in the entity_lookups table to
// find their canonical(s). Caches per-call so a query with N distinct
// entities issues at most N SQL lookups regardless of how many
// candidate rows the row loop walks.
//
// Implements the EntityResolver interface so the shared
// PromoteEntities helper runs at both teach and recall time without
// duplicating the resolver shape.
type CanonicalResolver struct {
	ctx   context.Context
	db    *sql.DB
	cache map[string][]string // lowercased entity -> distinct canonicals
}

// NewCanonicalResolver constructs a per-call canonical resolver.
// Cache is per-instance so concurrent recall/teach calls don't share
// stale lookups.
func NewCanonicalResolver(ctx context.Context, db *sql.DB) *CanonicalResolver {
	return &CanonicalResolver{ctx: ctx, db: db, cache: make(map[string][]string)}
}

// Resolve returns the canonical(s) for a single entity. A single token
// may map to multiple canonicals when the same alias exists across
// kinds (e.g., "Cards" -> Arizona Cardinals NFL + St. Louis Cardinals
// MLB). Empty slice when the entity has no row in entity_lookups.
//
// Errors (query failure, scan failure, rows.Err()) return nil and do
// NOT populate the cache. The next Resolve() call for the same token
// retries the SQL — a transient sqlite failure or partial scan must
// not pin a truncated canonical list for every subsequent lookup in
// this resolver's lifetime.
func (r *CanonicalResolver) Resolve(entity string) []string {
	key := strings.ToLower(strings.TrimSpace(entity))
	if key == "" {
		return nil
	}
	if cached, ok := r.cache[key]; ok {
		return cached
	}
	// Match against both `value` (alias side) and `canonical` (so an
	// already-canonical query like "United States" resolves to
	// itself). DISTINCT collapses multi-kind duplicates of the same
	// canonical.
	rows, err := r.db.QueryContext(r.ctx,
		`SELECT DISTINCT canonical FROM entity_lookups
		 WHERE LOWER(value) = ? OR LOWER(canonical) = ?`,
		key, key)
	if err != nil {
		// Do NOT cache; subsequent Resolve calls must retry rather
		// than returning a poisoned nil that suppresses a real
		// canonical the next time the DB is healthy.
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			// Don't cache partial results: a scan failure mid-loop
			// would otherwise pin a truncated canonical list for
			// every subsequent Resolve call in this recall, silently
			// suppressing real canonicals.
			return nil
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		// Same reason -- incomplete iteration must not be cached.
		return nil
	}
	r.cache[key] = out
	return out
}

// ResolveSet expands a slice of entities into a set of canonicals.
// Entries that don't resolve are dropped silently -- they simply don't
// contribute to the cross-alias matching score.
func (r *CanonicalResolver) ResolveSet(entities []string) map[string]struct{} {
	out := make(map[string]struct{})
	for _, e := range entities {
		for _, c := range r.Resolve(e) {
			out[c] = struct{}{}
		}
	}
	return out
}
