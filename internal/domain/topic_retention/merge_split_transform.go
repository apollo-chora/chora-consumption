// merge_split_transform.go — WS-C6 (CHO-2085, ADR-227 D14) retention side of a
// concept merge/split.
//
// On MERGE the survivor concept keeps the WEAKEST retention of the two merged
// concepts (WeakerOf) — the conservative, anti-farm pick that mirrors the
// campaign ladder's AND-of-rungs: a merged concept is only as "warm" as its
// coldest constituent. On both merge and split the winning curve is re-keyed
// onto the new concept id (CloneForTopic) so the KG map's due-terrain (the ⏰
// aligned on DueThreshold) follows the survivor / children.
//
// Pure + clock-injected, consistent with topic_retention.go: no wall-clock
// reads, no mutation of inputs.
package topic_retention

import (
	"fmt"
	"strings"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain"
)

// WeakerOf returns whichever score retains LESS at now (the lower RetentionAt)
// — the ADR-227 D14 "weakest retention" pick for a merge survivor. Nil-safe:
// (nil,nil) -> nil, exactly one nil -> the other. Ties resolve to a. Reads
// only (RetentionAt is pure) — neither input is mutated.
func WeakerOf(a, b *TopicScore, now time.Time) *TopicScore {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	if b.RetentionAt(now) < a.RetentionAt(now) {
		return b
	}
	return a
}

// CloneForTopic mints a fresh TopicScore for topicID carrying src's curve state
// (Strength, RetentionScore, ReviewCount, LastReviewedAt) — used to re-key
// retention onto a merge survivor or split children. The clone gets a fresh
// ScoreID, src's tenant/gcid, UpdatedAt=now, and is a live row (DeletedAt nil).
// A nil src or blank topicID is rejected with ErrInvalidScore.
func CloneForTopic(src *TopicScore, topicID string, now time.Time) (*TopicScore, error) {
	if src == nil {
		return nil, fmt.Errorf("%w: source score required", ErrInvalidScore)
	}
	if strings.TrimSpace(topicID) == "" {
		return nil, fmt.Errorf("%w: topic_id required", ErrInvalidScore)
	}
	return &TopicScore{
		ScoreID:        domain.NewUUIDv7(),
		TenantID:       src.TenantID,
		GCID:           src.GCID,
		TopicID:        topicID,
		Strength:       src.Strength,
		RetentionScore: src.RetentionScore,
		ReviewCount:    src.ReviewCount,
		LastReviewedAt: src.LastReviewedAt,
		UpdatedAt:      now,
	}, nil
}
