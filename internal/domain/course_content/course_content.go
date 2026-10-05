// Package course_content is the read-side projection of a Course's
// heterogeneous curriculum in chora-consumption (CHO-1612).
//
// It is the consumer counterpart of chora-delivery's course_content aggregate:
// chora-consumption never owns course content — it projects the
// chora.delivery.course.content_composed.v1 event (full ordered item list,
// replace-by-course) so the open-course learn page can render the curriculum.
//
// Atom-centric (CLAUDE.md §1): an item REFERENCES an atom/assessment/classroom
// by id or external media by URL; the projection holds no atom content.
//
// HEXAGONAL: dependency-free w.r.t. infrastructure (stdlib only).
package course_content

import (
	"errors"
	"strings"
)

// Kind mirrors chora.delivery.v1.ContentKind (lowercase wire values).
type Kind string

const (
	KindAtom          Kind = "atom"
	KindVideo         Kind = "video"
	KindYouTube       Kind = "youtube"
	KindDocument      Kind = "document"
	KindLiveClassroom Kind = "live_classroom"
	KindAssessment    Kind = "assessment"
)

// Valid reports whether k is a recognised content kind.
func (k Kind) Valid() bool {
	switch k {
	case KindAtom, KindVideo, KindYouTube, KindDocument, KindLiveClassroom, KindAssessment:
		return true
	}
	return false
}

// ErrInvalidItem is returned by New when a projected item is malformed.
var ErrInvalidItem = errors.New("course_content: invalid item")

// Item is one projected, ordered curriculum entry.
type Item struct {
	ItemID   string
	TenantID string
	CourseID string
	Kind     Kind
	Ref      string
	Title    string
	Position int
}

// NewParams is the constructor input for New.
type NewParams struct {
	ItemID   string
	TenantID string
	CourseID string
	Kind     Kind
	Ref      string
	Title    string
	Position int
}

// New validates + constructs a projected Item. The projection is lenient on
// ref shape (the producer already validated it) but requires the core fields.
func New(p NewParams) (*Item, error) {
	if strings.TrimSpace(p.TenantID) == "" {
		return nil, errors.New("course_content: tenant_id required")
	}
	if strings.TrimSpace(p.CourseID) == "" {
		return nil, errors.New("course_content: course_id required")
	}
	if strings.TrimSpace(p.ItemID) == "" {
		return nil, errors.New("course_content: item_id required")
	}
	if !p.Kind.Valid() {
		return nil, ErrInvalidItem
	}
	if strings.TrimSpace(p.Ref) == "" {
		return nil, errors.New("course_content: ref required")
	}
	if p.Position < 0 {
		return nil, errors.New("course_content: position must be >= 0")
	}
	return &Item{
		ItemID:   p.ItemID,
		TenantID: p.TenantID,
		CourseID: p.CourseID,
		Kind:     p.Kind,
		Ref:      strings.TrimSpace(p.Ref),
		Title:    strings.TrimSpace(p.Title),
		Position: p.Position,
	}, nil
}
