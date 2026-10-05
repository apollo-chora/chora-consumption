// maps_roads.go: the Roads layer on the map read (C4, UX Track U plan §8).
//
// A ROAD is one course-bound LearningPath that touches a concept: the
// intersection of the path's ordered AtomIDs with the concept's AtomRefs. Both
// sides live in chora_consumption, so this is an IN-DOMAIN join. It needs no new
// projection, and above all no cross-DB read into chora_delivery, which is
// forbidden (.claude/rules/ddd-enforcement.md hard rule #1).
//
// The course NAME comes from `course_directory` (migration 0072), the
// course_id → title read model this database already keeps, fed over Pub/Sub
// from chora.delivery.course.{created,updated}.v1. That is what lets the drawer
// say "also on Algebra I, module 2" rather than quoting a UUID at the learner.
//
// # Three ways this layer could lie, and what stops each
//
//  1. Naming a study list a certificate. Only a path with a CourseID is a road;
//     an ad-hoc or collection-derived path is the learner's own list and is
//     excluded, however many atoms it shares.
//
//  2. Inventing a course name. `course_directory` is a projection and can
//     legitimately have no row yet. No row means the road carries `courseId` and
//     NO title, and the SPA resolves the name from the courses read it already
//     holds. The path's own label is NOT a substitute: learning_path/bootstrap.go
//     defaults an untitled enrolment to the literal "Course " + CourseID, which
//     reads like a name and is not one, so that exact string is withheld.
//
//  3. Reporting "on no certificate path" from a read that never happened. An
//     unwired or erroring paths repo sets `roadsPartial` on the response instead
//     of serving an empty roads[]. Empty and unread are different facts, the same
//     distinction `leadPartial` draws on GET /v1/me/goals and `campaign` draws by
//     omitting its block. The map still renders: this overlay never 5xxs.
package http

import (
	"context"
	"log"
	"sort"
	"strings"

	"github.com/apollo-chora/chora-consumption/internal/domain/learning_path"
)

// conceptRoadDTO is one course-bound path touching one concept (camelCase, wire).
//
// AtomPosition is 1-based and is the position of the FIRST of this concept's
// atoms on the path, the drawer's "module N". ⚠ It is an ATOM ORDINAL, not a
// module number: a LearningPath is a flat ordered atom list and carries no module
// structure at all, so N is the nearest honest thing the data supports.
type conceptRoadDTO struct {
	PathID   string `json:"pathId"`
	CourseID string `json:"courseId"`
	// CourseTitle is the course_directory title. Empty when the projection has
	// no row for this course; never a fabricated or placeholder name.
	CourseTitle string `json:"courseTitle,omitempty"`
	// PathLabel is the path's OWN label when it is a real one. Empty when the
	// path carries the "Course <uuid>" bootstrap placeholder.
	PathLabel    string `json:"pathLabel,omitempty"`
	AtomPosition int    `json:"atomPosition"`
	AtomCount    int    `json:"atomCount"`
}

// roadTouch accumulates one path's overlap with one concept while scanning.
type roadTouch struct {
	pathIdx  int
	position int // 1-based, the earliest overlapping atom seen so far
	count    int
}

// attachRoads paints roads[] onto the already-built concept list, in place.
//
// Returns partial=true when the layer could NOT be computed, which the caller
// echoes as `roadsPartial`. Partial always leaves every roads[] empty rather
// than half-painted: a partly-painted layer would be indistinguishable from a
// complete one on the concepts that happened to be painted.
func (s *ExtServer) attachRoads(ctx context.Context, tenantID, gcid string, concepts []conceptNodeDTO) bool {
	if s.Paths == nil {
		// Not an error: a deployment without the paths repo has no source for
		// this layer. Still partial, because "no roads" would be a claim.
		return true
	}
	paths, err := s.Paths.ListByLearner(ctx, tenantID, gcid, 0)
	if err != nil {
		log.Printf("consumption: map roads layer OMITTED, paths read failed (tenant=%s gcid=%s): %v", tenantID, gcid, err)
		return true
	}

	roads := courseBoundPaths(paths)
	if len(roads) == 0 {
		return false // a real answer: the learner is on no course-bound path
	}
	byAtom := indexRoadAtoms(roads)
	titles := s.lookupCourseTitles(ctx, roadCourseIDs(roads))

	for i := range concepts {
		concepts[i].Roads = roadsForConcept(concepts[i].AtomRefs, roads, byAtom, titles)
	}
	return false
}

// courseBoundPaths keeps only the paths that are roads: live, course-bound, and
// carrying atoms. A path with no CourseID is the learner's own list.
func courseBoundPaths(paths []*learning_path.LearningPath) []*learning_path.LearningPath {
	out := make([]*learning_path.LearningPath, 0, len(paths))
	for _, p := range paths {
		if p == nil || p.DeletedAt != nil {
			continue
		}
		if strings.TrimSpace(p.CourseID) == "" || len(p.AtomIDs) == 0 {
			continue
		}
		out = append(out, p)
	}
	return out
}

// indexRoadAtoms maps every atom on every road to the roads carrying it and the
// 1-based position it sits at. A duplicate atom inside one path keeps the
// EARLIEST position: the drawer points at where the road first reaches the
// concept, and a later repeat does not move that.
func indexRoadAtoms(roads []*learning_path.LearningPath) map[string]map[int]int {
	byAtom := make(map[string]map[int]int, len(roads)*8)
	for idx, p := range roads {
		for pos, atomID := range p.AtomIDs {
			if atomID == "" {
				continue
			}
			at := byAtom[atomID]
			if at == nil {
				at = map[int]int{}
				byAtom[atomID] = at
			}
			if prev, seen := at[idx]; !seen || pos+1 < prev {
				at[idx] = pos + 1
			}
		}
	}
	return byAtom
}

// roadCourseIDs lists the distinct course ids behind the roads, for one batched
// directory lookup rather than one per concept.
func roadCourseIDs(roads []*learning_path.LearningPath) []string {
	seen := make(map[string]struct{}, len(roads))
	ids := make([]string, 0, len(roads))
	for _, p := range roads {
		if _, dup := seen[p.CourseID]; dup {
			continue
		}
		seen[p.CourseID] = struct{}{}
		ids = append(ids, p.CourseID)
	}
	return ids
}

// roadsForConcept intersects one concept's atom refs with the road index.
//
// The result is ordered by atomPosition then pathId, so two renders of the same
// data agree: an unordered map walk would reshuffle the drawer's list on every
// read for a concept sitting on two roads.
func roadsForConcept(atomRefs []string, roads []*learning_path.LearningPath, byAtom map[string]map[int]int, titles map[string]string) []conceptRoadDTO {
	if len(atomRefs) == 0 {
		return nil
	}
	touched := map[int]*roadTouch{}
	for _, atomID := range atomRefs {
		for idx, pos := range byAtom[atomID] {
			t := touched[idx]
			if t == nil {
				touched[idx] = &roadTouch{pathIdx: idx, position: pos, count: 1}
				continue
			}
			t.count++
			if pos < t.position {
				t.position = pos
			}
		}
	}
	if len(touched) == 0 {
		return nil
	}
	out := make([]conceptRoadDTO, 0, len(touched))
	for _, t := range touched {
		p := roads[t.pathIdx]
		out = append(out, conceptRoadDTO{
			PathID:       p.PathID,
			CourseID:     p.CourseID,
			CourseTitle:  titles[p.CourseID],
			PathLabel:    realPathLabel(p),
			AtomPosition: t.position,
			AtomCount:    t.count,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].AtomPosition != out[j].AtomPosition {
			return out[i].AtomPosition < out[j].AtomPosition
		}
		return out[i].PathID < out[j].PathID
	})
	return out
}

// realPathLabel returns the path's own title only when it is a real one.
//
// learning_path/bootstrap.go sets Title to "Course " + CourseID when the
// enrolment event carried no title. That string is a placeholder wearing a
// title's clothes: served as a label it would print a raw UUID to the learner
// and, worse, look like a resolved course name to the caller.
func realPathLabel(p *learning_path.LearningPath) string {
	title := strings.TrimSpace(p.Title)
	if title == "" || title == "Course "+p.CourseID {
		return ""
	}
	return title
}

