// rollup.go — read-time Growth-Edge rollup.
//
// learner_weakness.go promises (and migration 0046 documents) that concepts are
// "rolled up at read time by category / tag / topic". Write-time dedup
// (embedding cosine >= 0.9) already folds NEAR-IDENTICAL concepts; this is the
// COARSER read-time pass: distinct-but-sibling concepts on the same
// category/topic bucket collapse to one representative so the A+ Growth-Edges
// list shows one "Scrum" card, not five near-duplicate ones.
//
// Pure domain — no I/O, no clock. The List repository port stays raw (the daily
// dose, KG paint + chat overlays drill individual concepts); only the
// learner-facing list endpoint rolls up, via ListAll -> RollupPage.
package learner_weakness

import (
	"encoding/base64"
	"sort"
	"strconv"
	"strings"
)

// Read-time rollup page-size bounds — mirror the OpenAPI page_size enum
// [10,20,50,100] (default 20) and the pg List adapter's clamps.
const (
	DefaultRollupPageSize = 20
	MaxRollupPageSize     = 100
)

// rollupKey is the grouping bucket for read-time rollup: the composite
// (lower(category), topic_id). Granularity is FLAT (no parent/child) per the
// aggregate contract. An edge with NEITHER a category NOR a topic has no bucket
// — it is keyed by its own id so it always stands alone (otherwise every
// unkeyed edge would collapse into a single card).
func rollupKey(e LearnerWeakness) string {
	cat := strings.ToLower(strings.TrimSpace(e.Category))
	topic := strings.TrimSpace(e.TopicID)
	if cat == "" && topic == "" {
		return "id:" + e.ID // standalone — never merges with another row
	}
	return "c:" + cat + "\x00t:" + topic
}

// Rollup collapses near-duplicate Growth Edges that share the same
// (category, topic) bucket into a single representative, preserving the order in
// which each bucket first appears. The representative is the strongest member
// (max strength; ties broken by most-recent evidence then id); its facets are
// enriched from the whole group — strength = max, first_seen = min,
// last_evidenced = max, tags + sources = union, status = recomputed from the
// max strength — while keeping the strongest member's identity, descriptor and
// cached drill atoms (single-concept, clean for the card).
func Rollup(items []LearnerWeakness) []LearnerWeakness {
	if len(items) == 0 {
		return nil
	}
	order := make([]string, 0, len(items))
	groups := make(map[string][]LearnerWeakness, len(items))
	for _, e := range items {
		k := rollupKey(e)
		if _, seen := groups[k]; !seen {
			order = append(order, k)
		}
		groups[k] = append(groups[k], e)
	}
	out := make([]LearnerWeakness, 0, len(order))
	for _, k := range order {
		out = append(out, foldGroup(groups[k]))
	}
	return out
}

// foldGroup reduces one rollup bucket to its representative.
func foldGroup(group []LearnerWeakness) LearnerWeakness {
	rep := group[0]
	for _, e := range group[1:] {
		if strongerThan(e, rep) {
			rep = e
		}
	}
	// out keeps the strongest member's identity + descriptor + cached drills.
	// out.Strength is already the group MAX (the representative is, by selection,
	// the strongest member), so only the span timestamps + facet sets aggregate.
	out := rep
	out.Tags = cleanTags(rep.Tags)
	out.Sources = append([]Source(nil), rep.Sources...)
	for _, e := range group {
		if e.ID == rep.ID {
			continue
		}
		out.Tags = unionTags(out.Tags, cleanTags(e.Tags))
		for _, s := range e.Sources {
			out.Sources = unionSources(out.Sources, s)
		}
		if e.FirstSeenAt.Before(out.FirstSeenAt) {
			out.FirstSeenAt = e.FirstSeenAt
		}
		if e.LastEvidencedAt.After(out.LastEvidencedAt) {
			out.LastEvidencedAt = e.LastEvidencedAt
		}
		if e.UpdatedAt.After(out.UpdatedAt) {
			out.UpdatedAt = e.UpdatedAt
		}
	}
	out.Status = statusFor(out.Strength)
	return out
}

// strongerThan reports whether a should win representative selection over b:
// higher strength, then more-recent evidence, then larger id (deterministic).
func strongerThan(a, b LearnerWeakness) bool {
	if a.Strength != b.Strength {
		return a.Strength > b.Strength
	}
	if !a.LastEvidencedAt.Equal(b.LastEvidencedAt) {
		return a.LastEvidencedAt.After(b.LastEvidencedAt)
	}
	return a.ID > b.ID
}

// RollupPage rolls up the full filtered set, sorts the representatives, then
// returns the requested page via an opaque offset cursor. The repo's ListAll
// must supply EVERY matching live edge (not a raw page) so rollup buckets that
// straddle raw-page boundaries collapse correctly before pagination.
func RollupPage(all []LearnerWeakness, sortBy ListSort, pageToken string, pageSize int) ListResult {
	rolled := Rollup(all)
	sortEdges(rolled, sortBy)

	size := clampRollupPageSize(pageSize)
	offset := decodeRollupOffset(pageToken)
	if offset >= len(rolled) {
		return ListResult{Items: []LearnerWeakness{}}
	}
	end := offset + size
	next := ""
	if end < len(rolled) {
		next = encodeRollupOffset(end)
	} else {
		end = len(rolled)
	}
	return ListResult{Items: rolled[offset:end], NextPageToken: next}
}

// sortEdges orders representatives to mirror the pg List ORDER BY (the chosen
// key DESC, then id DESC as a stable tie-break).
func sortEdges(items []LearnerWeakness, sortBy ListSort) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		switch sortBy {
		case SortLastEvidencedDesc:
			if !a.LastEvidencedAt.Equal(b.LastEvidencedAt) {
				return a.LastEvidencedAt.After(b.LastEvidencedAt)
			}
		case SortFirstSeenDesc:
			if !a.FirstSeenAt.Equal(b.FirstSeenAt) {
				return a.FirstSeenAt.After(b.FirstSeenAt)
			}
		default: // SortStrengthDesc + unset
			if a.Strength != b.Strength {
				return a.Strength > b.Strength
			}
		}
		return a.ID > b.ID
	})
}

func clampRollupPageSize(n int) int {
	if n <= 0 {
		return DefaultRollupPageSize
	}
	if n > MaxRollupPageSize {
		return MaxRollupPageSize
	}
	return n
}

// encodeRollupOffset / decodeRollupOffset — opaque base64 offset cursor over the
// rolled-up list (mirrors the pg adapter's raw-row cursor; small per-learner
// counts make offset paging correct + sufficient).
func encodeRollupOffset(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte("o:" + strconv.Itoa(offset)))
}

func decodeRollupOffset(tok string) int {
	if tok == "" {
		return 0
	}
	b, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil {
		return 0
	}
	s := string(b)
	if !strings.HasPrefix(s, "o:") {
		return 0
	}
	n, err := strconv.Atoi(s[2:])
	if err != nil || n < 0 {
		return 0
	}
	return n
}
