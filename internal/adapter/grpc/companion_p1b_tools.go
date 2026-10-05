// companion_p1b_tools.go — CHO-2013 P1.B (R4-2/R4-3): the runtime tool
// allowlist merge + the two agent-tool backing RPCs.
//
//   - allowed_tools (CompanionInstanceConfig field 22): innate(stage) ladder
//     names mapped to canonical agent tool names ∪ the equipped active
//     Skills' tool_handler_refs, deduplicated + sorted. The agent's per-turn
//     filter keys on this list; allowed_skills stays the equipped skill KEYS.
//   - ReadLearnerProfile: read-only ADR-200 Global LearnerProfile slice for
//     the `profile.read` tool (progress_mirror).
//   - RecordCompanionMemoryNote: one learner-visible memory note through the
//     existing companion.Record port for the `memory.note` tool
//     (recap_scribe). Failures are LOUD — the write IS the point.
package grpc

import (
	"context"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
	lp "github.com/apollo-chora/chora-consumption/internal/domain/learner_profile"
	consumptionv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/consumption/v1"
)

// SkillCatalogueReader expands equipped skill keys to their tool bindings.
// Satisfied by companion.LoadoutRepository (pg + inmem).
type SkillCatalogueReader interface {
	ListCatalogue(ctx context.Context) (map[string]companion.CatalogEntry, error)
}

// LearnerProfileReader is the ADR-200 projection read slice.
// Satisfied by the pg LearnerProfileRepo.
type LearnerProfileReader interface {
	ListFacts(ctx context.Context, tenantID, learnerGCID string) ([]*lp.Fact, error)
	RecentActivity(ctx context.Context, tenantID, learnerGCID string, limit int) ([]*lp.ActivityEntry, error)
}

// CourseDirectoryReader is the OPTIONAL course_id → title resolver
// (course_directory projection; CHO-2059 follow-up). Satisfied by the pg
// CourseDirectoryRepo. nil ⇒ ReadLearnerProfile renders the leak-free
// type-generic noun ("a course") EXACTLY as before — the projection is purely
// additive.
type CourseDirectoryReader interface {
	LookupTitles(ctx context.Context, courseIDs []string) (map[string]string, error)
}

// CompanionMemoryRecorder is the companion memory Record port slice.
type CompanionMemoryRecorder interface {
	Record(ctx context.Context, in companion.RecordMemoryInput) error
}

// WithSkillCatalogueReader injects the catalogue for the allowed_tools
// expansion (nil ⇒ equipped Skills contribute no tools; innate still flows).
func WithSkillCatalogueReader(c SkillCatalogueReader) CompanionGrowthOption {
	return func(s *CompanionGrowthServer) { s.catalogue = c }
}

// WithLearnerProfileReader injects the ADR-200 projection reader
// (nil ⇒ ReadLearnerProfile returns Unimplemented).
func WithLearnerProfileReader(r LearnerProfileReader) CompanionGrowthOption {
	return func(s *CompanionGrowthServer) { s.profiles = r }
}

// WithCourseDirectoryReader injects the OPTIONAL course_id → title resolver
// (course_directory projection, CHO-2059). nil ⇒ ReadLearnerProfile keeps the
// leak-free generic-noun output (the projection is purely additive).
func WithCourseDirectoryReader(r CourseDirectoryReader) CompanionGrowthOption {
	return func(s *CompanionGrowthServer) { s.courseDir = r }
}

// WithCompanionMemoryNote injects the memory Record port + embedder for
// RecordCompanionMemoryNote (nil ⇒ Unimplemented).
func WithCompanionMemoryNote(m CompanionMemoryRecorder, e companion.Embedder, embeddingModelID string) CompanionGrowthOption {
	return func(s *CompanionGrowthServer) {
		s.memoryNotes = m
		s.embedder = e
		s.embeddingModelID = embeddingModelID
	}
}

// innateToolNameMap translates the growth curve's SHIPPED innate ladder
// names to the agent registry's canonical tool names (ADR-249 A1a: the
// agent ships exactly atom.cite). A ladder name that is neither mapped here
// nor declared in growth.UnshippedLadderTools is a vocabulary drift and
// fails the merge loudly instead of vanishing into the agent's
// narrowing-safe WARN.
var innateToolNameMap = map[string]string{
	"cite_atom": "atom.cite",
}

// mergeAllowedTools computes the runtime tool allowlist (R4-2):
// innate(stage) mapped names ∪ the equipped Skills' tool_handler_refs,
// deduplicated + sorted. An equipped key absent from the catalogue is data
// corruption (grants are minted FROM catalogue rows) → error. Declared
// unshipped innate names (ADR-249) are omitted with a debug note, so the
// agent's allowlist advertises only tools that exist; equipped catalogue
// refs keep the R4-2 pass-through (the agent narrows them safely).
func mergeAllowedTools(innate []string, equippedKeys []string, catalogue map[string]companion.CatalogEntry) ([]string, error) {
	seen := map[string]bool{}
	var droppedUnshipped []string
	for _, ladder := range innate {
		if growth.UnshippedLadderTools[ladder] {
			droppedUnshipped = append(droppedUnshipped, ladder)
			continue
		}
		name, ok := innateToolNameMap[ladder]
		if !ok {
			return nil, status.Errorf(codes.Internal,
				"innate ladder name %q neither maps to a shipped agent tool nor is declared unshipped (ADR-249 A1a agree-invariant)",
				ladder)
		}
		seen[name] = true
	}
	if len(droppedUnshipped) > 0 {
		log.Printf("DEBUG p1b merge: innate ladder names declared unshipped (ADR-249 A1a), omitted from allowed_tools: %v",
			droppedUnshipped)
	}
	for _, key := range equippedKeys {
		entry, ok := catalogue[key]
		if !ok {
			return nil, status.Errorf(codes.Internal,
				"equipped skill %q has no catalogue row (grant/catalogue drift)", key)
		}
		for _, ref := range entry.ToolHandlerRefs {
			ref = strings.TrimSpace(ref)
			if ref != "" {
				seen[ref] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

// profileWindowCutoff maps the request window to a cutoff time; zero time =
// no filter ("all", the default).
func profileWindowCutoff(window string, now time.Time) time.Time {
	switch strings.TrimSpace(window) {
	case "week":
		return now.AddDate(0, 0, -7)
	case "month":
		return now.AddDate(0, -1, 0)
	default:
		return time.Time{}
	}
}

// loadOwnedCompanion loads the instance + enforces (tenant, caller) ownership.
// Foreign == missing (no cross-owner disclosure).
func (s *CompanionGrowthServer) loadOwnedCompanion(ctx context.Context, tenantID, companionID, callerGCID string) (*companion.Instance, error) {
	inst, err := s.instances.Get(ctx, companionID)
	if err != nil || inst == nil || inst.TenantID != tenantID || inst.OwnerGCID != callerGCID {
		return nil, status.Error(codes.NotFound, "companion not found")
	}
	return inst, nil
}

// ReadLearnerProfile implements consumptionv1.CompanionGrowthServer (P1.B).
func (s *CompanionGrowthServer) ReadLearnerProfile(
	ctx context.Context,
	req *consumptionv1.ReadLearnerProfileRequest,
) (*consumptionv1.ReadLearnerProfileResponse, error) {
	ctx, span := tracer().Start(ctx, "CompanionGrowth.ReadLearnerProfile")
	defer span.End()
	span.SetAttributes(
		attribute.String("chora.tenant_id", req.GetTenantId()),
		attribute.String("chora.companion_id", req.GetCompanionId()),
	)
	if s.instances == nil || s.profiles == nil {
		return nil, status.Error(codes.Unimplemented,
			"learner profile reader not wired (WithLearnerProfileReader)")
	}
	tenantID, companionID, callerGCID := strings.TrimSpace(req.GetTenantId()), strings.TrimSpace(req.GetCompanionId()), strings.TrimSpace(req.GetCallerGcid())
	if tenantID == "" || companionID == "" || callerGCID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id, companion_id, caller_gcid required")
	}
	ctx = tracing.WithGCID(tracing.WithTenantID(ctx, tenantID), callerGCID)

	if _, err := s.loadOwnedCompanion(ctx, tenantID, companionID, callerGCID); err != nil {
		return nil, err
	}

	facts, err := s.profiles.ListFacts(ctx, tenantID, callerGCID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list profile facts: %v", err)
	}
	activity, err := s.profiles.RecentActivity(ctx, tenantID, callerGCID, 20)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list profile activity: %v", err)
	}

	// CHO-2059: batch-resolve real course NAMES from the course_directory
	// projection so the agent reflects "Algebra I" instead of "a course". Nil
	// reader ⇒ empty map ⇒ resolvedName="" ⇒ byte-identical generic-noun output.
	titles := s.resolveCourseTitles(ctx, facts, activity)

	cutoff := profileWindowCutoff(req.GetWindow(), time.Now().UTC())
	resp := &consumptionv1.ReadLearnerProfileResponse{}
	for _, f := range facts {
		if f == nil || (!cutoff.IsZero() && f.OccurredAt.Before(cutoff)) {
			continue
		}
		// Learner-SAFE projection: Ref/Title/DetailJson never carry a raw
		// cross-domain ref UUID (course/cert id) — the agent would otherwise
		// parrot it into its reflection (domain-vocabulary violation). A resolved
		// course name (when the directory knows it) WINS over the generic noun.
		// See learner_profile/presentation.go.
		resolved := titles[lp.CourseIDForFact(f)]
		resp.Facts = append(resp.Facts, &consumptionv1.LearnerProfileFact{
			FactType:   string(f.Type),
			Ref:        lp.SafeFactRef(f),
			Title:      lp.FactDisplayLabel(f, resolved),
			DetailJson: factDetailJSON(f, resolved),
			OccurredAt: timestamppb.New(f.OccurredAt),
		})
	}
	for _, a := range activity {
		if a == nil || (!cutoff.IsZero() && a.OccurredAt.Before(cutoff)) {
			continue
		}
		resp.RecentActivity = append(resp.RecentActivity, &consumptionv1.LearnerProfileActivity{
			ActivityType: a.Kind,
			Summary:      lp.ActivityDisplay(a, titles[lp.CourseIDForActivity(a)]),
			OccurredAt:   timestamppb.New(a.OccurredAt),
		})
	}
	return resp, nil
}

// resolveCourseTitles batch-resolves course_id → title for the supplied facts +
// activity through the optional CourseDirectory reader. A nil reader — or no
// course references — yields an empty map, so the presenter receives
// resolvedName="" and the output is byte-identical to the pre-projection
// behaviour. A reader error is logged LOUD but is non-fatal: the enrichment is
// additive and must never fail the profile read.
func (s *CompanionGrowthServer) resolveCourseTitles(ctx context.Context, facts []*lp.Fact, activity []*lp.ActivityEntry) map[string]string {
	if s.courseDir == nil {
		return map[string]string{}
	}
	ids := lp.CollectCourseIDs(facts, activity)
	if len(ids) == 0 {
		return map[string]string{}
	}
	titles, err := s.courseDir.LookupTitles(ctx, ids)
	if err != nil {
		log.Printf("consumption: ReadLearnerProfile course-title resolve failed (non-fatal, additive enrichment): %v", err)
		return map[string]string{}
	}
	return titles
}

// factDetailJSON renders the projection Detail verbatim-ish for the agent
// tool (compact, no fabrication — absent fields stay absent). resolvedName is
// the course title resolved by the caller (CHO-2059); it flows into the label
// exactly as the Title does, so the detail JSON and the Title stay consistent.
func factDetailJSON(f *lp.Fact, resolvedName string) string {
	parts := make([]string, 0, 4)
	// learner-safe label (never a raw cross-domain UUID), mirroring the Title.
	if label := lp.FactDisplayLabel(f, resolvedName); label != "" {
		parts = append(parts, `"label":`+strconvQuote(label))
	}
	if f.Detail.Score != nil {
		parts = append(parts, `"score":`+trimFloat(*f.Detail.Score))
	}
	if f.Detail.Passed != nil {
		if *f.Detail.Passed {
			parts = append(parts, `"passed":true`)
		} else {
			parts = append(parts, `"passed":false`)
		}
	}
	if len(parts) == 0 {
		return "{}"
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// closedNoteTypes — the R4-3 closed set. Extending it = a CR ruling, not a
// code tweak (the memory view + ADR-215 steer surfaces key off note types).
var closedNoteTypes = map[string]bool{"recap": true}

const maxMemoryNoteChars = 2000

// RecordCompanionMemoryNote implements consumptionv1.CompanionGrowthServer (P1.B).
func (s *CompanionGrowthServer) RecordCompanionMemoryNote(
	ctx context.Context,
	req *consumptionv1.RecordCompanionMemoryNoteRequest,
) (*consumptionv1.RecordCompanionMemoryNoteResponse, error) {
	ctx, span := tracer().Start(ctx, "CompanionGrowth.RecordCompanionMemoryNote")
	defer span.End()
	span.SetAttributes(
		attribute.String("chora.tenant_id", req.GetTenantId()),
		attribute.String("chora.companion_id", req.GetCompanionId()),
	)
	if s.instances == nil || s.memoryNotes == nil || s.embedder == nil {
		return nil, status.Error(codes.Unimplemented,
			"companion memory note not wired (WithCompanionMemoryNote)")
	}
	tenantID, companionID, callerGCID := strings.TrimSpace(req.GetTenantId()), strings.TrimSpace(req.GetCompanionId()), strings.TrimSpace(req.GetCallerGcid())
	if tenantID == "" || companionID == "" || callerGCID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id, companion_id, caller_gcid required")
	}
	noteType := strings.TrimSpace(req.GetNoteType())
	if !closedNoteTypes[noteType] {
		return nil, status.Errorf(codes.InvalidArgument, "note_type %q not in the closed set (v1: recap)", noteType)
	}
	content := strings.TrimSpace(req.GetContent())
	if content == "" {
		return nil, status.Error(codes.InvalidArgument, "content required")
	}
	if len(content) > maxMemoryNoteChars {
		return nil, status.Errorf(codes.InvalidArgument, "content exceeds %d chars", maxMemoryNoteChars)
	}
	ctx = tracing.WithGCID(tracing.WithTenantID(ctx, tenantID), callerGCID)

	if _, err := s.loadOwnedCompanion(ctx, tenantID, companionID, callerGCID); err != nil {
		return nil, err
	}

	vec, err := s.embedder.Embed(ctx, companion.EmbedInput{
		Text: content, TaskType: companion.EmbedTaskDocument, TenantID: tenantID,
	})
	if err != nil {
		// LOUD: unlike the chat handler's after-the-turn soft-fail, this
		// RPC's whole purpose is the durable note — the Skill must know.
		return nil, status.Errorf(codes.Internal, "embed memory note: %v", err)
	}
	if err := s.memoryNotes.Record(ctx, companion.RecordMemoryInput{
		TenantID:    tenantID,
		OwnerGCID:   callerGCID,
		CompanionID: companionID,
		MemoryType:  noteType,
		ContentText: content,
		Embedding:   vec,
		ModelID:     s.embeddingModelID,
		Now:         time.Now().UTC(),
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "record memory note: %v", err)
	}
	return &consumptionv1.RecordCompanionMemoryNoteResponse{Recorded: true}, nil
}

// strconvQuote / trimFloat — tiny local JSON helpers.
func strconvQuote(s string) string {
	return strconv.Quote(s)
}

func trimFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}
