// eventbus_handlers.go — adapters that project chora-common eventbus deliveries
// onto the subscriber methods. Kept in the subscribers package (not events)
// because subscribers import events for the Envelope type; the reverse import
// would be a cycle.
//
// These replace the retired Pub/Sub push HTTP handlers: the eventbus carries
// the same envelope (decoded from JetStream headers) + payload bytes, so each
// adapter does what the push dispatch did — decode the payload, stamp the
// RLS tenant/gcid on the ctx, call the subscriber's Handle — minus the OIDC
// push verification, which has no NATS equivalent (subscribers dial the broker
// directly; the network boundary is the NATS authorization layer).
package subscribers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/encoding/protowire"

	"github.com/apollo-chora/chora-common/envelope"
	"github.com/apollo-chora/chora-common/eventbus"
	"github.com/apollo-chora/chora-common/tracing"

	consumptionv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/consumption/v1"
	creationv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/creation/v1"
	paymentsv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/payments/v1"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/adapter/events/protodecode"
	"github.com/apollo-chora/chora-consumption/internal/domain/learner_profile"
	wu "github.com/apollo-chora/chora-consumption/internal/domain/weakness_upload"
)

// projectEnvelope projects the wire envelope.Envelope onto the local
// events.Envelope. The eventbus decode path populates every mandatory field
// from the message headers; the projection is field-for-field.
func projectEnvelope(env envelope.Envelope) events.Envelope {
	return events.Envelope{
		EventID:        env.EventID,
		IdempotencyKey: env.IdempotencyKey,
		TenantID:       env.TenantID,
		GCID:           env.GCID,
		OccurredAt:     env.OccurredAt,
		PublishedAt:    env.PublishedAt,
		Traceparent:    env.Traceparent,
		Tracestate:     env.Tracestate,
		SourceProject:  env.SourceProject,
		SourceService:  env.SourceService,
		SchemaVersion:  env.SchemaVersion,
	}
}

// ---------------------------------------------------------------------------
// Shared field helpers (relocated from the retired push handlers)
// ---------------------------------------------------------------------------

func strField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func boolField(m map[string]any, key string) bool {
	if m == nil {
		return false
	}
	if v, ok := m[key].(bool); ok {
		return v
	}
	return false
}

func intField(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case float32:
		return int(v)
	case int:
		return v
	case int32:
		return int(v)
	case int64:
		return int(v)
	}
	return 0
}

func numField(m map[string]any, key string) (float64, bool) {
	switch v := m[key].(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	}
	return 0, false
}

func floatField(m map[string]any, key string) float64 {
	if v, ok := numField(m, key); ok {
		return v
	}
	return 0
}

func floatPtrField(m map[string]any, key string) *float64 {
	if v, ok := numField(m, key); ok {
		return &v
	}
	return nil
}

func int64Field(m map[string]any, key string) int64 {
	switch v := m[key].(type) {
	case float64:
		return int64(v)
	case float32:
		return int64(v)
	case int:
		return int64(v)
	case int32:
		return int64(v)
	case int64:
		return v
	}
	return 0
}

func strSliceField(m map[string]any, key string) []string {
	arr, ok := m[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, v := range arr {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func stringSliceField(m map[string]any, key string) []string {
	return strSliceField(m, key)
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// parseTimeField reads an RFC3339 / RFC3339Nano timestamp string from the
// decoded map. A missing/unparseable value yields the zero time — the
// subscriber then falls back to the envelope's occurred_at.
func parseTimeField(m map[string]any, key string) time.Time {
	v := strField(m, key)
	if v == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return t
	}
	return time.Time{}
}

func parseContentItems(v any) []CourseContentItemPayload {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]CourseContentItemPayload, 0, len(arr))
	for _, e := range arr {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, CourseContentItemPayload{
			ItemID:   strField(m, "item_id"),
			Kind:     strField(m, "kind"),
			Ref:      strField(m, "ref"),
			Title:    strField(m, "title"),
			Position: intField(m, "position"),
		})
	}
	return out
}

// preferenceKVsFromRaw parses the decoded `preferences` array (each element a
// {key,value} object) into domain PreferenceKV pairs.
func preferenceKVsFromRaw(v any) []learner_profile.PreferenceKV {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]learner_profile.PreferenceKV, 0, len(arr))
	for _, it := range arr {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		key, _ := m["key"].(string)
		val, _ := m["value"].(string)
		out = append(out, learner_profile.PreferenceKV{Key: key, Value: val})
	}
	return out
}

// ---------------------------------------------------------------------------
// Cross-domain projections (retired cross_domain_pubsub_handler.go)
// ---------------------------------------------------------------------------

const (
	TopicDeliveryEnrollmentCreated = "chora.delivery.enrollment.created.v1"
	TopicCreationAtomCreated       = "chora.creation.atom.created.v1"
	TopicCreationAtomPublished     = "chora.creation.atom.published.v1"
	TopicCreationAtomUpdated       = "chora.creation.atom.updated.v1"
)

// EnrollmentCreatedHandler adapts the EnrollmentCreatedSubscriber to an
// eventbus.Handler.
func EnrollmentCreatedHandler(s *EnrollmentCreatedSubscriber) eventbus.Handler {
	return func(ctx context.Context, msg eventbus.Message) error {
		if s == nil {
			return errors.New("subscribers: enrollment_created handler not initialised")
		}
		env := projectEnvelope(msg.Envelope)
		raw, err := protodecode.DecodePayloadMap(TopicDeliveryEnrollmentCreated, msg.Payload)
		if err != nil {
			return fmt.Errorf("enrollment_created: %w", err)
		}
		p := EnrollmentCreatedPayload{
			EnrollmentID: strField(raw, "enrollment_id"),
			CourseID:     strField(raw, "course_id"),
			LearnerGCID:  strField(raw, "learner_gcid"),
			TenantID:     strField(raw, "tenant_id"),
		}
		if p.TenantID == "" {
			p.TenantID = env.TenantID
		}
		if p.LearnerGCID == "" {
			p.LearnerGCID = env.GCID
		}
		if v := strField(raw, "enrolled_at"); v != "" {
			if t, parseErr := time.Parse(time.RFC3339Nano, v); parseErr == nil {
				p.EnrolledAt = t
			}
		}
		if p.EnrolledAt.IsZero() {
			p.EnrolledAt = env.OccurredAt
		}
		// Set the RLS tenant session for the pg-backed atom_index repo from
		// the event's tenant (bus deliveries carry no tenant middleware ctx).
		return s.Handle(tracing.WithTenantID(ctx, p.TenantID), env, p)
	}
}

// AtomCreatedHandler adapts the AtomCreatedSubscriber to an eventbus.Handler.
func AtomCreatedHandler(s *AtomCreatedSubscriber) eventbus.Handler {
	return func(ctx context.Context, msg eventbus.Message) error {
		if s == nil {
			return errors.New("subscribers: atom_created handler not initialised")
		}
		env := projectEnvelope(msg.Envelope)
		raw, err := protodecode.DecodePayloadMap(TopicCreationAtomCreated, msg.Payload)
		if err != nil {
			return fmt.Errorf("atom_created: %w", err)
		}
		p := AtomCreatedPayload{
			AtomID:          strField(raw, "atom_id"),
			TenantID:        strField(raw, "tenant_id"),
			CourseID:        strField(raw, "course_id"),
			Title:           strField(raw, "title"),
			AtomType:        strField(raw, "atom_type"),
			Difficulty:      intField(raw, "difficulty"),
			TopicTags:       stringSliceField(raw, "topic_tags"),
			CorrectOptionID: strField(raw, "correct_option_id"),
			AnswerCount:     intField(raw, "answer_count"),
		}
		if p.TenantID == "" {
			p.TenantID = env.TenantID
		}
		if v := strField(raw, "published_at"); v != "" {
			if t, parseErr := time.Parse(time.RFC3339Nano, v); parseErr == nil {
				p.PublishedAt = t
			}
		}
		if p.PublishedAt.IsZero() {
			p.PublishedAt = env.OccurredAt
		}
		return s.Handle(tracing.WithTenantID(ctx, p.TenantID), env, p)
	}
}

// AtomPublishedHandler adapts the AtomPublishedSubscriber (+ the optional
// ADR-244 D5 refresh fan-out) to an eventbus.Handler.
func AtomPublishedHandler(s *AtomPublishedSubscriber, refresh *AtomRefreshSubscriber) eventbus.Handler {
	return func(ctx context.Context, msg eventbus.Message) error {
		if s == nil {
			return errors.New("subscribers: atom_published handler not initialised")
		}
		env := projectEnvelope(msg.Envelope)
		raw, err := protodecode.DecodePayloadMap(TopicCreationAtomPublished, msg.Payload)
		if err != nil {
			return fmt.Errorf("atom_published: %w", err)
		}
		p := AtomPublishedIndexPayload{
			AtomID:               strField(raw, "atom_id"),
			TenantID:             strField(raw, "tenant_id"),
			AtomType:             strField(raw, "atom_type"),
			Status:               strField(raw, "status"),
			CorrectOptionID:      strField(raw, "correct_option_id"),
			AnswerCount:          intField(raw, "answer_count"),
			HasOpenEndedQuestion: boolField(raw, "has_open_ended_question"),
			CognitiveLevel:       strField(raw, "cognitive_level"),
		}
		if p.TenantID == "" {
			p.TenantID = env.TenantID
		}
		tctx := tracing.WithTenantID(ctx, p.TenantID)
		if err := s.Handle(tctx, env, p); err != nil {
			return err
		}
		// ADR-244 D5 fan-out AFTER the flip so the refresh reads a projection
		// that already says published/servable. Both halves are idempotent, so
		// an error here nacks and the redelivery re-runs both safely.
		if refresh != nil {
			return refresh.Handle(tctx, AtomRefreshTrigger{
				TenantID:    p.TenantID,
				AtomID:      p.AtomID,
				EventID:     env.EventID,
				Traceparent: env.Traceparent,
				Tracestate:  env.Tracestate,
				Reason:      AtomRefreshReasonPublished,
			})
		}
		return nil
	}
}

// AtomUpdatedHandler adapts the AtomRefreshSubscriber to an eventbus.Handler.
func AtomUpdatedHandler(refresh *AtomRefreshSubscriber) eventbus.Handler {
	return func(ctx context.Context, msg eventbus.Message) error {
		if refresh == nil {
			return errors.New("subscribers: atom_updated handler not initialised")
		}
		env := projectEnvelope(msg.Envelope)
		raw, err := protodecode.DecodePayloadMap(TopicCreationAtomUpdated, msg.Payload)
		if err != nil {
			return fmt.Errorf("atom_updated: %w", err)
		}
		tenantID := strField(raw, "tenant_id")
		if tenantID == "" {
			tenantID = env.TenantID
		}
		return refresh.Handle(tracing.WithTenantID(ctx, tenantID), AtomRefreshTrigger{
			TenantID:    tenantID,
			AtomID:      strField(raw, "atom_id"),
			EventID:     env.EventID,
			Traceparent: env.Traceparent,
			Tracestate:  env.Tracestate,
			Reason:      AtomRefreshReasonUpdated,
		})
	}
}

// ---------------------------------------------------------------------------
// Course content / metadata projections
// ---------------------------------------------------------------------------

const (
	TopicDeliveryCourseContentComposed = "chora.delivery.course.content_composed.v1"
	TopicDeliveryCourseCreated          = "chora.delivery.course.created.v1"
	TopicDeliveryCourseReleased         = "chora.delivery.course.released.v1"
)

// CourseContentComposedHandler adapts the CourseContentComposedSubscriber to
// an eventbus.Handler.
func CourseContentComposedHandler(s *CourseContentComposedSubscriber) eventbus.Handler {
	return func(ctx context.Context, msg eventbus.Message) error {
		if s == nil {
			return errors.New("subscribers: course_content_composed handler not initialised")
		}
		env := projectEnvelope(msg.Envelope)
		raw, err := protodecode.DecodePayloadMap(TopicDeliveryCourseContentComposed, msg.Payload)
		if err != nil {
			return fmt.Errorf("course_content_composed: %w", err)
		}
		// Prefer the payload tenant_id so the projection key is correct even
		// if the envelope header is absent.
		if tid := strField(raw, "tenant_id"); tid != "" {
			env.TenantID = tid
		}
		return s.Handle(env, CourseContentComposedPayload{
			CourseID: strField(raw, "course_id"),
			Items:    parseContentItems(raw["items"]),
		})
	}
}

// CourseMetadataHandler adapts the CourseMetadataSubscriber to an
// eventbus.Handler. The subscription may carry both course.created.v1 and
// course.released.v1; the subject selects the decoder.
func CourseMetadataHandler(s *CourseMetadataSubscriber) eventbus.Handler {
	return func(ctx context.Context, msg eventbus.Message) error {
		if s == nil {
			return errors.New("subscribers: course_metadata handler not initialised")
		}
		decodeTopic := TopicDeliveryCourseCreated
		if msg.Subject == TopicDeliveryCourseReleased {
			decodeTopic = TopicDeliveryCourseReleased
		}
		env := projectEnvelope(msg.Envelope)
		raw, err := protodecode.DecodePayloadMap(decodeTopic, msg.Payload)
		if err != nil {
			return fmt.Errorf("course_metadata: %w", err)
		}
		if tid := strField(raw, "tenant_id"); tid != "" {
			env.TenantID = tid
		}
		return s.Handle(env, CourseMetadataPayload{
			CourseID: strField(raw, "course_id"),
			Title:    strField(raw, "title"),
		})
	}
}

// ---------------------------------------------------------------------------
// Learning-path topics → active_path_topics (CHO-1662)
// ---------------------------------------------------------------------------

const (
	TopicLearningPathBootstrapped = "chora.consumption.learning_path.bootstrapped.v1"
	TopicLearningPathAdvanced     = "chora.consumption.learning_path.advanced.v1"
)

// LearningPathTopicsHandler adapts the ActivePathTopicsSubscriber to an
// eventbus.Handler. The subscription carries both bootstrapped.v1 and
// advanced.v1; the subject selects the decode + handle path.
func LearningPathTopicsHandler(s *ActivePathTopicsSubscriber) eventbus.Handler {
	return func(ctx context.Context, msg eventbus.Message) error {
		if s == nil {
			return errors.New("subscribers: learning_path_topics handler not initialised")
		}
		decodeTopic := TopicLearningPathBootstrapped
		if msg.Subject == TopicLearningPathAdvanced {
			decodeTopic = TopicLearningPathAdvanced
		}
		env := projectEnvelope(msg.Envelope)
		raw, err := protodecode.DecodePayloadMap(decodeTopic, msg.Payload)
		if err != nil {
			return fmt.Errorf("learning_path_topics: decode payload: %w", err)
		}

		tenantID := strField(raw, "tenant_id")
		if tenantID == "" {
			tenantID = env.TenantID
		}
		learnerGCID := strField(raw, "learner_gcid")
		if learnerGCID == "" {
			learnerGCID = env.GCID
		}

		// Stamp the RLS tenant + gcid: bus deliveries carry no tenant
		// middleware, and the pg adapter's rls.ApplySession reads them off the
		// ctx. Without this the RLS policies on active_path_topics reject
		// every INSERT.
		ctx = tracing.WithGCID(tracing.WithTenantID(ctx, tenantID), learnerGCID)

		if decodeTopic == TopicLearningPathAdvanced {
			return s.HandleAdvance(ctx, env, LearningPathAdvancedPayload{
				PathID:       strField(raw, "path_id"),
				CourseID:     strField(raw, "course_id"),
				LearnerGCID:  learnerGCID,
				TenantID:     tenantID,
				AtomID:       strField(raw, "atom_id"),
				CurrentIndex: intField(raw, "current_index"),
				TotalAtoms:   intField(raw, "total_atoms"),
				OccurredAt:   env.OccurredAt,
			})
		}

		return s.HandleBootstrap(ctx, env, LearningPathBootstrappedPayload{
			PathID:       strField(raw, "path_id"),
			CourseID:     strField(raw, "course_id"),
			LearnerGCID:  learnerGCID,
			TenantID:     tenantID,
			EnrollmentID: strField(raw, "enrollment_id"),
			AtomIDs:      strSliceField(raw, "atom_ids"),
			OccurredAt:   env.OccurredAt,
		})
	}
}

// ---------------------------------------------------------------------------
// LearnerProfile read-model (ADR-200) + StudentTranscript fan-out (W6)
// ---------------------------------------------------------------------------

// LearnerProfileHandler adapts the LearnerProfileSubscriber to an
// eventbus.Handler for one topic. The transcript fan-out rides the
// cert-issued + submission-graded legs.
func LearnerProfileHandler(s *LearnerProfileSubscriber, transcript *TranscriptSubscriber, topic string) eventbus.Handler {
	return func(ctx context.Context, msg eventbus.Message) error {
		if s == nil {
			return errors.New("subscribers: learner_profile handler not initialised")
		}
		env := projectEnvelope(msg.Envelope)
		raw, err := protodecode.DecodePayloadMap(topic, msg.Payload)
		if err != nil {
			return fmt.Errorf("learner_profile %s: %w", topic, err)
		}
		if tid := strField(raw, "tenant_id"); tid != "" {
			env.TenantID = tid
		}
		switch topic {
		case "chora.delivery.certification.issued.v1":
			if err := s.HandleCertIssued(env, CertIssuedPayload{
				CertID:      strField(raw, "cert_id"),
				CourseID:    strField(raw, "course_id"),
				LearnerGCID: strField(raw, "learner_gcid"),
				IssuedAt:    parseTimeField(raw, "issued_at"),
			}); err != nil {
				return err
			}
			if transcript != nil {
				return transcript.HandleCertIssued(env, TranscriptCertIssuedPayload{
					CertID:      strField(raw, "cert_id"),
					CourseID:    strField(raw, "course_id"),
					LearnerGCID: strField(raw, "learner_gcid"),
					IssuedAt:    parseTimeField(raw, "issued_at"),
				})
			}
			return nil
		case "chora.consumption.learning_path.completed.v1":
			return s.HandlePathCompleted(env, PathCompletedPayload{
				PathID:      strField(raw, "path_id"),
				LearnerGCID: strField(raw, "learner_gcid"),
				CompletedAt: parseTimeField(raw, "completed_at"),
			})
		case "chora.delivery.enrollment.created.v1":
			return s.HandleEnrollmentCreated(env, EnrollmentCreatedPayload{
				EnrollmentID: strField(raw, "enrollment_id"),
				CourseID:     strField(raw, "course_id"),
				LearnerGCID:  strField(raw, "learner_gcid"),
				TenantID:     env.TenantID,
				EnrolledAt:   parseTimeField(raw, "enrolled_at"),
			})
		case "chora.delivery.submission.graded.v1":
			var score *float64
			if possible := intField(raw, "total_points_possible"); possible > 0 {
				sc := floatField(raw, "total_points_earned") / float64(possible) * 100
				score = &sc
			}
			var passed *bool
			if _, ok := raw["passed"]; ok {
				p := boolField(raw, "passed")
				passed = &p
			}
			if err := s.HandleSubmissionGraded(env, SubmissionGradedPayload{
				SubmissionID: strField(raw, "submission_id"),
				LearnerGCID:  strField(raw, "learner_gcid"),
				Score:        score,
				Passed:       passed,
				OccurredAt:   parseTimeField(raw, "graded_at"),
			}); err != nil {
				return err
			}
			if transcript != nil {
				return transcript.HandleSubmissionGraded(env, TranscriptSubmissionGradedPayload{
					SubmissionID: strField(raw, "submission_id"),
					AssessmentID: strField(raw, "assessment_id"),
					AssessmentTitle: strField(raw, "assessment_title"),
					DeliveryType:  strField(raw, "delivery_type"),
					LearnerGCID:   strField(raw, "learner_gcid"),
					ScoreEarned:   floatPtrField(raw, "total_points_earned"),
					ScorePossible: floatPtrField(raw, "total_points_possible"),
					Passed:        passed,
					OccurredAt:    parseTimeField(raw, "graded_at"),
				})
			}
			return nil
		case "chora.delivery.enrollment.completed.v1":
			return s.HandleEnrollmentCompleted(env, EnrollmentCompletedPayload{
				CourseID:    strField(raw, "course_id"),
				LearnerGCID: strField(raw, "learner_gcid"),
				Passed:      boolField(raw, "passed"),
				CompletedAt: parseTimeField(raw, "completed_at"),
			})
		case "chora.consumption.preferences.updated.v1":
			return s.HandlePreferencesUpdated(env, PreferencesUpdatedPayload{
				LearnerGCID: strField(raw, "learner_gcid"),
				Prefs:       preferenceKVsFromRaw(raw["preferences"]),
				OccurredAt:  parseTimeField(raw, "occurred_at"),
			})
		}
		return nil
	}
}

// ---------------------------------------------------------------------------
// Collection → study list (WS-4)
// ---------------------------------------------------------------------------


// CollectionConvertedToStudyListHandler adapts the
// CollectionConvertedToStudyListSubscriber to an eventbus.Handler.
func CollectionConvertedToStudyListHandler(s *CollectionConvertedToStudyListSubscriber) eventbus.Handler {
	return func(ctx context.Context, msg eventbus.Message) error {
		if s == nil {
			return errors.New("subscribers: collection_converted handler not initialised")
		}
		env := projectEnvelope(msg.Envelope)
		raw, err := protodecode.DecodePayloadMap(TopicCollectionConvertedToStudyList, msg.Payload)
		if err != nil {
			return fmt.Errorf("collection_converted: decode payload: %w", err)
		}
		if tid := strField(raw, "tenant_id"); tid != "" {
			env.TenantID = tid
		}
		if g := strField(raw, "gcid"); g != "" && env.GCID == "" {
			env.GCID = g
		}
		owner := strField(raw, "owner_gcid")
		if owner == "" {
			owner = env.GCID
		}
		return s.Handle(env, CollectionConvertedToStudyListPayload{
			CollectionID:     strField(raw, "collection_id"),
			OwnerGCID:        owner,
			AtomIDs:          strSliceField(raw, "atom_ids"),
			StudyListEventID: strField(raw, "study_list_event_id"),
		})
	}
}

// ---------------------------------------------------------------------------
// Concept cascade + suggestion curation
// ---------------------------------------------------------------------------

const TopicConceptDeleted = "chora.consumption.concept.deleted.v1"

// ConceptDeletedHandler adapts the ConceptDeletedSubscriber to an
// eventbus.Handler.
func ConceptDeletedHandler(s *ConceptDeletedSubscriber) eventbus.Handler {
	return func(ctx context.Context, msg eventbus.Message) error {
		if s == nil {
			return errors.New("subscribers: concept_deleted handler not initialised")
		}
		env := projectEnvelope(msg.Envelope)
		conceptID, tenantID, learnerGCID, err := decodeConceptDeletedPayload(msg.Payload)
		if err != nil {
			return fmt.Errorf("concept_deleted: decode payload: %w", err)
		}
		if tenantID == "" {
			tenantID = env.TenantID
		}
		if learnerGCID == "" {
			learnerGCID = env.GCID
		}
		return s.Handle(env, ConceptDeletedPayload{
			ConceptID:   conceptID,
			TenantID:    tenantID,
			LearnerGCID: learnerGCID,
		})
	}
}

// decodeConceptDeletedPayload hand-decodes the concept_id / tenant_id /
// learner_gcid fields from the binary concept.deleted.v1 body.
func decodeConceptDeletedPayload(data []byte) (conceptID, tenantID, learnerGCID string, err error) {
	if len(data) == 0 {
		return "", "", "", errors.New("empty payload")
	}
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return "", "", "", protowire.ParseError(n)
		}
		data = data[n:]
		if typ == protowire.BytesType {
			v, m := protowire.ConsumeBytes(data)
			if m < 0 {
				return "", "", "", protowire.ParseError(m)
			}
			data = data[m:]
			switch num {
			case 2:
				conceptID = string(v)
			case 3:
				tenantID = string(v)
			case 4:
				learnerGCID = string(v)
			}
			continue
		}
		m := protowire.ConsumeFieldValue(num, typ, data)
		if m < 0 {
			return "", "", "", protowire.ParseError(m)
		}
		data = data[m:]
	}
	return conceptID, tenantID, learnerGCID, nil
}

const TopicConceptSuggestionEmitted = "chora.consumption.concept_suggestion.emitted.v1"

// ConceptSuggestionEmittedHandler adapts the ConceptSuggestionEmittedSubscriber
// to an eventbus.Handler.
func ConceptSuggestionEmittedHandler(s *ConceptSuggestionEmittedSubscriber) eventbus.Handler {
	return func(ctx context.Context, msg eventbus.Message) error {
		if s == nil {
			return errors.New("subscribers: concept_suggestion_emitted handler not initialised")
		}
		env := projectEnvelope(msg.Envelope)
		p, err := decodeConceptSuggestionEmittedPayload(msg.Payload)
		if err != nil {
			return fmt.Errorf("concept_suggestion_emitted: decode payload: %w", err)
		}
		ctx = tracing.WithGCID(tracing.WithTenantID(ctx, env.TenantID), env.GCID)
		return s.Handle(ctx, env, p)
	}
}

// conceptSuggestionEmittedBody is the concept_suggestion.emitted.v1 JSON body
// shape (snake_case mirrors the proto).
type conceptSuggestionEmittedBody struct {
	TenantID    string `json:"tenant_id"`
	LearnerGCID string `json:"learner_gcid"`
	// PRE-RENAME WIRE KEY: the kennel's kg_exploration lane EMITS familiar_id
	// (fold_lanes.py result body); decoded by name here. Coordinated cut later.
	CompanionID    string `json:"familiar_id"`
	MapTheme       string `json:"map_theme"`
	FocalConceptID string `json:"focal_concept_id"`
	ModelUsed      string `json:"model_used"`
	RunID          string `json:"run_id"`
	Concepts       []struct {
		Title     string   `json:"title"`
		Rationale string   `json:"rationale"`
		AtomRefs  []string `json:"atom_refs"`
	} `json:"concepts"`
	Edges []struct {
		SourceConceptID string `json:"source_concept_id"`
		TargetConceptID string `json:"target_concept_id"`
		EdgeClass       string `json:"edge_class"`
		Rationale       string `json:"rationale"`
	} `json:"edges"`
}

func decodeConceptSuggestionEmittedPayload(data []byte) (ConceptSuggestionEmittedPayload, error) {
	if len(data) == 0 {
		return ConceptSuggestionEmittedPayload{}, errors.New("empty payload")
	}
	var b conceptSuggestionEmittedBody
	if err := json.Unmarshal(data, &b); err != nil {
		return ConceptSuggestionEmittedPayload{}, fmt.Errorf("json.Unmarshal ConceptSuggestionEmitted: %w", err)
	}
	concepts := make([]SuggestedConceptPayload, 0, len(b.Concepts))
	for _, c := range b.Concepts {
		concepts = append(concepts, SuggestedConceptPayload{
			Title: c.Title, Rationale: c.Rationale, AtomRefs: c.AtomRefs,
		})
	}
	edges := make([]SuggestedEdgePayload, 0, len(b.Edges))
	for _, e := range b.Edges {
		edges = append(edges, SuggestedEdgePayload{
			SourceConceptID: e.SourceConceptID, TargetConceptID: e.TargetConceptID,
			EdgeClass: e.EdgeClass, Rationale: e.Rationale,
		})
	}
	return ConceptSuggestionEmittedPayload{
		TenantID:       b.TenantID,
		LearnerGCID:    b.LearnerGCID,
		CompanionID:    b.CompanionID,
		MapTheme:       b.MapTheme,
		FocalConceptID: b.FocalConceptID,
		ModelUsed:      b.ModelUsed,
		RunID:          b.RunID,
		Concepts:       concepts,
		Edges:          edges,
	}, nil
}

// ---------------------------------------------------------------------------
// Campaign (WS-C4 reveal + WS-C5 conquest XP)
// ---------------------------------------------------------------------------

const (
	TopicCampaignNodeWon       = "chora.consumption.campaign.node_won.v1"
	TopicCampaignRungCleared   = "chora.consumption.campaign.rung_cleared.v1"
	TopicCampaignGoalSealed    = "chora.consumption.campaign.goal_sealed.v1"
	TopicWeaknessGrown         = "chora.consumption.weakness.grown.v1"
	TopicWeaknessReviewPending = "chora.consumption.weakness.review_pending.v1"
)

// CampaignNodeWonHandler adapts the CampaignNodeWonSubscriber to an
// eventbus.Handler.
func CampaignNodeWonHandler(s *CampaignNodeWonSubscriber) eventbus.Handler {
	return func(ctx context.Context, msg eventbus.Message) error {
		if s == nil {
			return errors.New("subscribers: campaign_node_won handler not initialised")
		}
		p, err := decodeCampaignNodeWonPayload(msg.Payload)
		if err != nil {
			return fmt.Errorf("campaign_node_won: decode payload: %w", err)
		}
		ctx = tracing.WithGCID(tracing.WithTenantID(ctx, p.TenantID), p.LearnerGCID)
		return s.Handle(ctx, p)
	}
}

func decodeCampaignNodeWonPayload(data []byte) (CampaignNodeWonPayload, error) {
	if len(data) == 0 {
		return CampaignNodeWonPayload{}, errors.New("empty payload")
	}
	var m consumptionv1.CampaignNodeWon
	if err := proto.Unmarshal(data, &m); err != nil {
		return CampaignNodeWonPayload{}, fmt.Errorf("proto.Unmarshal CampaignNodeWon: %w", err)
	}
	env := m.GetEnvelope()
	if env == nil || strings.TrimSpace(env.GetEventId()) == "" {
		return CampaignNodeWonPayload{}, errors.New("CampaignNodeWon missing envelope.event_id")
	}
	return CampaignNodeWonPayload{
		TenantID:    firstNonEmpty(m.GetTenantId(), env.GetTenantId()),
		LearnerGCID: firstNonEmpty(m.GetLearnerGcid(), env.GetGcid()),
		GoalID:      m.GetGoalId(),
		ConceptID:   m.GetConceptId(),
		ConceptKey:  m.GetConceptKey(),
		EventID:     env.GetEventId(),
		Traceparent: env.GetTraceparent(),
		Tracestate:  env.GetTracestate(),
	}, nil
}

// CampaignXPHandler adapts the CampaignXPSubscriber to an eventbus.Handler.
// The subscription carries the three XP-bearing campaign topics; the subject
// selects the decode + handle path.
func CampaignXPHandler(s *CampaignXPSubscriber) eventbus.Handler {
	return func(ctx context.Context, msg eventbus.Message) error {
		if s == nil {
			return errors.New("subscribers: campaign_xp handler not initialised")
		}
		switch msg.Subject {
		case TopicCampaignRungCleared:
			p, err := decodeCampaignRungClearedPayload(msg.Payload)
			if err != nil {
				return fmt.Errorf("campaign_xp: decode rung_cleared: %w", err)
			}
			ctx = tracing.WithGCID(tracing.WithTenantID(ctx, p.TenantID), p.LearnerGCID)
			return s.HandleRungCleared(ctx, p)
		case TopicCampaignNodeWon:
			p, err := decodeCampaignNodeWonPayload(msg.Payload)
			if err != nil {
				return fmt.Errorf("campaign_xp: decode node_won: %w", err)
			}
			ctx = tracing.WithGCID(tracing.WithTenantID(ctx, p.TenantID), p.LearnerGCID)
			return s.HandleNodeWon(ctx, p)
		case TopicCampaignGoalSealed:
			p, err := decodeCampaignGoalSealedPayload(msg.Payload)
			if err != nil {
				return fmt.Errorf("campaign_xp: decode goal_sealed: %w", err)
			}
			ctx = tracing.WithGCID(tracing.WithTenantID(ctx, p.TenantID), p.LearnerGCID)
			return s.HandleGoalSealed(ctx, p)
		}
		return nil
	}
}

func decodeCampaignRungClearedPayload(data []byte) (CampaignRungClearedPayload, error) {
	if len(data) == 0 {
		return CampaignRungClearedPayload{}, errors.New("empty payload")
	}
	var m consumptionv1.CampaignRungCleared
	if err := proto.Unmarshal(data, &m); err != nil {
		return CampaignRungClearedPayload{}, fmt.Errorf("proto.Unmarshal CampaignRungCleared: %w", err)
	}
	env := m.GetEnvelope()
	if env == nil || strings.TrimSpace(env.GetEventId()) == "" {
		return CampaignRungClearedPayload{}, errors.New("CampaignRungCleared missing envelope.event_id")
	}
	return CampaignRungClearedPayload{
		TenantID:       firstNonEmpty(m.GetTenantId(), env.GetTenantId()),
		LearnerGCID:    firstNonEmpty(m.GetLearnerGcid(), env.GetGcid()),
		GoalID:         m.GetGoalId(),
		ConceptID:      m.GetConceptId(),
		ConceptKey:     m.GetConceptKey(),
		Rung:           int(m.GetRung()),
		IsRefresher:    m.GetIsRefresher(),
		CorrectAnswers: int(m.GetCorrectAnswers()),
		EventID:        env.GetEventId(),
		Traceparent:    env.GetTraceparent(),
		Tracestate:     env.GetTracestate(),
	}, nil
}

func decodeCampaignGoalSealedPayload(data []byte) (CampaignGoalSealedPayload, error) {
	if len(data) == 0 {
		return CampaignGoalSealedPayload{}, errors.New("empty payload")
	}
	var m consumptionv1.CampaignGoalSealed
	if err := proto.Unmarshal(data, &m); err != nil {
		return CampaignGoalSealedPayload{}, fmt.Errorf("proto.Unmarshal CampaignGoalSealed: %w", err)
	}
	env := m.GetEnvelope()
	if env == nil || strings.TrimSpace(env.GetEventId()) == "" {
		return CampaignGoalSealedPayload{}, errors.New("CampaignGoalSealed missing envelope.event_id")
	}
	return CampaignGoalSealedPayload{
		TenantID:       firstNonEmpty(m.GetTenantId(), env.GetTenantId()),
		LearnerGCID:    firstNonEmpty(m.GetLearnerGcid(), env.GetGcid()),
		GoalID:         m.GetGoalId(),
		RootConceptID:  m.GetRootConceptId(),
		RootConceptKey: m.GetRootConceptKey(),
		NodesWon:       int(m.GetNodesWon()),
		EventID:        env.GetEventId(),
		Traceparent:    env.GetTraceparent(),
		Tracestate:     env.GetTracestate(),
	}, nil
}

// ---------------------------------------------------------------------------
// Companion growth EXP + egg purchase (ADR-149 / ADR-228)
// ---------------------------------------------------------------------------

const (
	TopicPaymentsCompanionEggCaptured           = "chora.payments.companion_egg_purchase.payment_captured.v1"
	TopicSharingPostCreated                     = "chora.sharing.post.created.v1"
	TopicSharingReactionAdded                   = "chora.sharing.reaction.added.v1"
)

// CompanionGrowthHandler adapts the CompanionGrowthSubscriber (+ the optional
// W3-derived projector) to an eventbus.Handler. The subscription carries the
// growth-bearing topics; the subject selects the decode + handle path.
func CompanionGrowthHandler(s *CompanionGrowthSubscriber, derived *DerivedWeaknessProjector) eventbus.Handler {
	return func(ctx context.Context, msg eventbus.Message) error {
		if s == nil {
			return errors.New("subscribers: companion_growth handler not initialised")
		}
		topic := msg.Subject
		env := projectEnvelope(msg.Envelope)
		// RLS: the downstream growth repo + companion resolver run
		// rls.ApplySession, which reads the tenant from the CONTEXT. Bus
		// deliveries carry no tenant middleware, so stamp the envelope tenant
		// here or every growth event errors with rls.ErrNoTenantContext.
		ctx = tracing.WithTenantID(ctx, env.TenantID)
		// Inbound payloads MAY be binary protobuf OR JSON (legacy + in-flight
		// topics). protodecode handles both; failure surfaces a wrapped error
		// so the bus nacks for retry + DLQ.
		raw, err := protodecode.DecodePayloadMap(topic, msg.Payload)
		if err != nil {
			return fmt.Errorf("companion_growth: %s: %w", topic, err)
		}
		switch topic {
		case events.TopicAtomSessionCompleted:
			// CHO-2030 buildout: SessionCompletion's IMDA D1 evidence rides the
			// SAME topic with a fresh event_id (source_action=imda_evidence) —
			// it is an audit artefact, not a second completion. Ack it so it
			// never dead-letters; the growth + derived projectors both sit
			// behind this dispatch.
			if strField(raw, "source_action") == "imda_evidence" {
				return nil
			}
			p := AtomSessionCompletedPayload{
				SessionID:   strField(raw, "session_id"),
				AtomID:      strField(raw, "atom_id"),
				LearnerGCID: strField(raw, "learner_gcid"),
				TenantID:    strField(raw, "tenant_id"),
				IsCorrect:   boolField(raw, "answer_correct"),
				ReviewDue:   boolField(raw, "review_due"),
				Traceparent: strField(raw, "traceparent"),
				Tracestate:  strField(raw, "tracestate"),
			}
			if p.TenantID == "" {
				p.TenantID = env.TenantID
			}
			if err := s.HandleAtomSessionCompleted(ctx, env, p); err != nil {
				return err
			}
			// W3-derived: the same completion also feeds the derived
			// Growth-Edge projector (topic_accuracy + upsert/recover).
			if derived != nil {
				return derived.HandleAtomSessionCompleted(ctx, env, p)
			}
			return nil
		case events.TopicKGJunctionAccepted:
			return s.HandleJunctionAccepted(ctx, env, KGJunctionAcceptedPayload{
				LearnerGCID: strField(raw, "learner_gcid"),
			})
		case events.TopicDailyDoseServed:
			return s.HandleDailyDoseServed(ctx, env, DailyDoseServedPayload{
				LearnerGCID: strField(raw, "learner_gcid"),
			})
		case TopicSharingPostCreated:
			return s.HandlePostCreated(ctx, env, PostCreatedPayload{
				AuthorGCID: strField(raw, "author_gcid"),
			})
		case TopicSharingReactionAdded:
			return s.HandleReactionAdded(ctx, env, ReactionAddedPayload{
				TargetGCID: strField(raw, "target_gcid"),
			})
		case TopicCreationAtomPublished:
			return s.HandleAtomPublished(ctx, env, AtomPublishedPayload{
				AuthorGCID: strField(raw, "author_gcid"),
			})
		}
		// Unknown topic — ack so the bus doesn't keep redelivering.
		return nil
	}
}

// EggPurchaseHandler adapts the ProvisionEggSubscriber to an eventbus.Handler.
func EggPurchaseHandler(s *ProvisionEggSubscriber) eventbus.Handler {
	return func(ctx context.Context, msg eventbus.Message) error {
		if s == nil {
			return errors.New("subscribers: egg_purchase handler not initialised")
		}
		topic := msg.Subject
		if topic != TopicPaymentsCompanionEggCaptured {
			return nil
		}
		env := projectEnvelope(msg.Envelope)
		if len(msg.Payload) == 0 {
			return errors.New("egg_purchase: empty payload")
		}
		// chora-payments publishes this event as binary protobuf with the
		// EventEnvelope embedded in the payload. Decode the typed event
		// directly. The Companion owner is the payments learner_gcid (no
		// purchaser_gcid relay).
		ev := &paymentsv1.CompanionEggPurchasePaymentCaptured{}
		if err := proto.Unmarshal(msg.Payload, ev); err != nil {
			return fmt.Errorf("egg_purchase: proto.Unmarshal CompanionEggPurchasePaymentCaptured: %w", err)
		}
		paid := time.Time{}
		if ts := ev.GetPaidAt(); ts != nil {
			paid = ts.AsTime()
		}
		p := EggPaymentSucceededPayload{
			PurchaseID:            ev.GetPurchaseId(),
			PurchaserGCID:         ev.GetLearnerGcid(),
			TargetTenantID:        ev.GetTargetTenantId(),
			EggSku:                ev.GetEggSku(),
			StripeSessionID:       ev.GetStripeSessionId(),
			StripePaymentIntentID: ev.GetStripePaymentIntentId(),
			AmountCentsPaid:       ev.GetAmountCentsPaid(),
			Currency:              ev.GetCurrency(),
			SuggestedFocalAtomID:  ev.GetSuggestedFocalAtomId(),
			PaidAt:                paid,
		}
		// RLS: ProvisionEgg → GrowthRepo.ProvisionEgg runs rls.ApplySession(ctx),
		// which reads the tenant from the CONTEXT, and the INSERT writes
		// tenant_id = p.TargetTenantID into companion_instances. So the session
		// tenant MUST equal the egg's TARGET tenant — which may differ from
		// the envelope/payment tenant. Fall back to the envelope tenant only
		// when target_tenant_id is absent.
		provisionTenant := p.TargetTenantID
		if provisionTenant == "" {
			provisionTenant = env.TenantID
		}
		ctx = tracing.WithTenantID(ctx, provisionTenant)
		return s.Handle(ctx, env, p)
	}
}

// ---------------------------------------------------------------------------
// Derived weakness projector (W3) + live quiz + wave-1 XP
// ---------------------------------------------------------------------------

const (
	TopicLiveQuizScoreAwarded = "chora.delivery.live_quiz_session.score_awarded.v1"
	TopicSubmissionGraded     = "chora.delivery.submission.graded.v1"
)

// LiveQuizScoreHandler adapts the DerivedWeaknessProjector's live-quiz leg to
// an eventbus.Handler.
func LiveQuizScoreHandler(p *DerivedWeaknessProjector) eventbus.Handler {
	return func(ctx context.Context, msg eventbus.Message) error {
		if p == nil {
			return errors.New("subscribers: live_quiz_score handler not initialised")
		}
		env := projectEnvelope(msg.Envelope)
		raw, err := protodecode.DecodePayloadMap(TopicLiveQuizScoreAwarded, msg.Payload)
		if err != nil {
			return fmt.Errorf("live_quiz_score: %s: %w", TopicLiveQuizScoreAwarded, err)
		}
		payload := LiveQuizScoreAwardedPayload{
			SessionID:   strField(raw, "session_id"),
			LiveQuizID:  strField(raw, "live_quiz_id"),
			QuestionID:  strField(raw, "question_id"),
			AtomID:      strField(raw, "atom_id"),
			TopicTags:   stringSliceField(raw, "topic_tags"),
			Correct:     boolField(raw, "correct"),
			TenantID:    strField(raw, "tenant_id"),
			LearnerGCID: strField(raw, "learner_gcid"),
		}
		if payload.LearnerGCID == "" {
			payload.LearnerGCID = strField(raw, "gcid") // envelope gcid IS the learner
		}
		ctx = tracing.WithGCID(tracing.WithTenantID(ctx, env.TenantID), env.GCID)
		return p.HandleLiveQuizScoreAwarded(ctx, env, payload)
	}
}

// SubmissionGradedDerivedHandler adapts the DerivedWeaknessProjector's
// submission-graded leg (+ the flat wave-1 EXP award) to an eventbus.Handler.
func SubmissionGradedDerivedHandler(p *DerivedWeaknessProjector, xp *WaveOneXPSubscriber) eventbus.Handler {
	return func(ctx context.Context, msg eventbus.Message) error {
		if p == nil {
			return errors.New("subscribers: submission_graded_derived handler not initialised")
		}
		env := projectEnvelope(msg.Envelope)
		raw, err := protodecode.DecodePayloadMap(TopicSubmissionGraded, msg.Payload)
		if err != nil {
			return fmt.Errorf("submission_graded_derived: %s: %w", TopicSubmissionGraded, err)
		}
		payload := SubmissionGradedEvidence{
			SubmissionID:    strField(raw, "submission_id"),
			AssessmentID:    strField(raw, "assessment_id"),
			AssessmentTitle: strField(raw, "assessment_title"),
			LearnerGCID:     strField(raw, "learner_gcid"),
			TenantID:        strField(raw, "tenant_id"),
			PointsEarned:    floatField(raw, "total_points_earned"),
			PointsPossible:  intField(raw, "total_points_possible"),
		}
		if payload.LearnerGCID == "" {
			payload.LearnerGCID = strField(raw, "gcid") // envelope gcid IS the learner
		}
		if payload.TenantID == "" {
			payload.TenantID = env.TenantID
		}
		ctx = tracing.WithGCID(tracing.WithTenantID(ctx, env.TenantID), env.GCID)
		if err := p.HandleSubmissionGraded(ctx, env, payload); err != nil {
			return err
		}
		// F-I3 (CHO-2090): the same graded delivery awards the flat
		// submission_graded companion EXP.
		if xp != nil {
			return xp.HandleSubmissionGraded(ctx, env, payload)
		}
		return nil
	}
}

// ---------------------------------------------------------------------------
// Goal graduation + goal-knowledge lanes (§3 / CHO-2118)
// ---------------------------------------------------------------------------

// GoalGraduationHandler adapts the GoalGraduationSubscriber to an
// eventbus.Handler.
func GoalGraduationHandler(s *GoalGraduationSubscriber) eventbus.Handler {
	return func(ctx context.Context, msg eventbus.Message) error {
		if s == nil {
			return errors.New("subscribers: goal_graduation handler not initialised")
		}
		env := projectEnvelope(msg.Envelope)
		p, err := decodeWeaknessGrownPayload(msg.Payload)
		if err != nil {
			return fmt.Errorf("goal_graduation: decode payload: %w", err)
		}
		ctx = tracing.WithGCID(tracing.WithTenantID(ctx, env.TenantID), env.GCID)
		return s.Handle(ctx, env, p)
	}
}

func decodeWeaknessGrownPayload(data []byte) (WeaknessGrownPayload, error) {
	if len(data) == 0 {
		return WeaknessGrownPayload{}, errors.New("empty payload")
	}
	var m consumptionv1.WeaknessGrown
	if err := proto.Unmarshal(data, &m); err != nil {
		return WeaknessGrownPayload{}, fmt.Errorf("proto.Unmarshal WeaknessGrown: %w", err)
	}
	return WeaknessGrownPayload{
		GrowthEdgeID: m.GetGrowthEdgeId(),
		TenantID:     m.GetTenantId(),
		LearnerGCID:  m.GetLearnerGcid(),
		ConceptKey:   m.GetConceptKey(),
	}, nil
}

// GoalKnowledgeCompletionHandler adapts the GoalKnowledgeCompletionSubscriber
// to an eventbus.Handler.
func GoalKnowledgeCompletionHandler(s *GoalKnowledgeCompletionSubscriber) eventbus.Handler {
	return func(ctx context.Context, msg eventbus.Message) error {
		if s == nil {
			return errors.New("subscribers: goal_knowledge_completion handler not initialised")
		}
		env := projectEnvelope(msg.Envelope)
		if len(msg.Payload) == 0 {
			return errors.New("goal_knowledge_completion: empty payload")
		}
		var p events.GoalKnowledgeSynthesizedPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return fmt.Errorf("goal_knowledge_completion: decode json payload: %w", err)
		}
		ctx = tracing.WithGCID(tracing.WithTenantID(ctx, env.TenantID), env.GCID)
		return s.Handle(ctx, env, p)
	}
}

const (
	TopicGKWeaknessAnalyzed      = "chora.consumption.weakness.analyzed.v1"
	TopicGKGoalProgressUpdated   = "chora.consumption.goal.progress_updated.v1"
	TopicGKGoalGraduated         = "chora.consumption.goal.graduated.v1"
	TopicGKChatTurnCompleted     = "chora.consumption.companion.chat_turn_completed.v1"
	TopicGKCompanionMemoryEvicts = "chora.consumption.companion.memory_eviction.v1"
)

// GoalKnowledgeInvalidationHandler adapts the
// GoalKnowledgeInvalidationSubscriber to an eventbus.Handler. The subscription
// carries the six invalidation topics; the subject selects the decode path.
func GoalKnowledgeInvalidationHandler(s *GoalKnowledgeInvalidationSubscriber) eventbus.Handler {
	return func(ctx context.Context, msg eventbus.Message) error {
		if s == nil {
			return errors.New("subscribers: goal_knowledge_invalidation handler not initialised")
		}
		topic := msg.Subject
		if !isGoalKnowledgeTopic(topic) {
			return nil
		}
		env := projectEnvelope(msg.Envelope)
		if len(msg.Payload) == 0 {
			return fmt.Errorf("goal_knowledge_invalidation: empty payload on %s", topic)
		}
		ctx = tracing.WithGCID(tracing.WithTenantID(ctx, env.TenantID), env.GCID)

		switch topic {
		case TopicWeaknessGrown:
			var m consumptionv1.WeaknessGrown
			if err := proto.Unmarshal(msg.Payload, &m); err != nil {
				return fmt.Errorf("goal_knowledge_invalidation: proto.Unmarshal WeaknessGrown: %w", err)
			}
			return s.HandleWeaknessGrown(ctx, env, WeaknessGrownPayload{
				GrowthEdgeID: m.GetGrowthEdgeId(),
				TenantID:     firstNonEmpty(m.GetTenantId(), env.TenantID),
				LearnerGCID:  firstNonEmpty(m.GetLearnerGcid(), env.GCID),
				ConceptKey:   m.GetConceptKey(),
			})
		case TopicGKWeaknessAnalyzed:
			var m consumptionv1.WeaknessAnalyzed
			if err := proto.Unmarshal(msg.Payload, &m); err != nil {
				return fmt.Errorf("goal_knowledge_invalidation: proto.Unmarshal WeaknessAnalyzed: %w", err)
			}
			edges := make([]ExtractedGrowthEdgePayload, 0, len(m.GetEdges()))
			for _, e := range m.GetEdges() {
				edges = append(edges, ExtractedGrowthEdgePayload{
					ConceptKey:   e.GetConceptKey(),
					ConceptLabel: e.GetConceptLabel(),
				})
			}
			return s.HandleWeaknessAnalyzed(ctx, env, WeaknessAnalyzedPayload{
				UploadID:    m.GetUploadId(),
				TenantID:    firstNonEmpty(m.GetTenantId(), env.TenantID),
				LearnerGCID: firstNonEmpty(m.GetLearnerGcid(), env.GCID),
				Edges:       edges,
			})
		case TopicGKGoalProgressUpdated:
			var m consumptionv1.GoalProgressUpdated
			if err := proto.Unmarshal(msg.Payload, &m); err != nil {
				return fmt.Errorf("goal_knowledge_invalidation: proto.Unmarshal GoalProgressUpdated: %w", err)
			}
			return s.HandleGoalProgressUpdated(ctx, env, GoalProgressUpdatedPayload{
				GoalID:      m.GetGoalId(),
				TenantID:    firstNonEmpty(m.GetTenantId(), env.TenantID),
				LearnerGCID: firstNonEmpty(m.GetLearnerGcid(), env.GCID),
			})
		case TopicGKGoalGraduated:
			var m consumptionv1.GoalGraduated
			if err := proto.Unmarshal(msg.Payload, &m); err != nil {
				return fmt.Errorf("goal_knowledge_invalidation: proto.Unmarshal GoalGraduated: %w", err)
			}
			return s.HandleGoalGraduated(ctx, env, GoalGraduatedPayload{
				GoalID:      m.GetGoalId(),
				TenantID:    firstNonEmpty(m.GetTenantId(), env.TenantID),
				LearnerGCID: firstNonEmpty(m.GetLearnerGcid(), env.GCID),
			})
		case TopicGKChatTurnCompleted:
			var m consumptionv1.CompanionChatTurnCompleted
			if err := proto.Unmarshal(msg.Payload, &m); err != nil {
				return fmt.Errorf("goal_knowledge_invalidation: proto.Unmarshal CompanionChatTurnCompleted: %w", err)
			}
			return s.HandleChatTurnCompleted(ctx, env, CompanionChatTurnCompletedPayload{
				CompanionID: m.GetCompanionId(),
				TenantID:    firstNonEmpty(m.GetTenantId(), env.TenantID),
				LearnerGCID: firstNonEmpty(m.GetGcid(), env.GCID),
			})
		case TopicGKCompanionMemoryEvicts:
			var m consumptionv1.CompanionMemoryEviction
			if err := proto.Unmarshal(msg.Payload, &m); err != nil {
				return fmt.Errorf("goal_knowledge_invalidation: proto.Unmarshal CompanionMemoryEviction: %w", err)
			}
			// This proto carries no tenant_id — the envelope is the only source.
			return s.HandleMemoryEviction(ctx, env, CompanionMemoryEvictionPayload{
				CompanionID: m.GetCompanionId(),
				TenantID:    env.TenantID,
				OwnerGCID:   firstNonEmpty(m.GetOwnerGcid(), env.GCID),
			})
		}
		return nil
	}
}

func isGoalKnowledgeTopic(topic string) bool {
	switch topic {
	case TopicWeaknessGrown, TopicGKWeaknessAnalyzed,
		TopicGKGoalProgressUpdated, TopicGKGoalGraduated,
		TopicGKChatTurnCompleted, TopicGKCompanionMemoryEvicts:
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// KG fog-cache invalidation (ADR-143 / ADR-204 F.1)
// ---------------------------------------------------------------------------

// KGInvalidationHandler adapts the three KG fog-cache invalidation subscribers
// to an eventbus.Handler. The subscription carries atom.published +
// atom.updated + user_retention.shifted; the subject selects the subscriber.
func KGInvalidationHandler(atomPublished *AtomPublishedKGSubscriber, atomRevisionUpdated *AtomRevisionUpdatedKGSubscriber, userRetentionShifted *UserRetentionShiftedKGSubscriber) eventbus.Handler {
	return func(ctx context.Context, msg eventbus.Message) error {
		topic := msg.Subject
		env := projectEnvelope(msg.Envelope)
		raw, err := protodecode.DecodePayloadMap(topic, msg.Payload)
		if err != nil {
			return fmt.Errorf("kg_invalidation: %s: %w", topic, err)
		}
		// Set the RLS tenant session for the pg-backed hexagon repo from the
		// event's tenant (bus deliveries carry no tenant middleware ctx).
		tenantID := strField(raw, "tenant_id")
		if tenantID == "" {
			tenantID = env.TenantID
		}
		tctx := tracing.WithTenantID(ctx, tenantID)

		switch topic {
		case TopicCreationAtomPublished:
			if atomPublished == nil {
				return nil
			}
			return atomPublished.Handle(tctx, env, AtomPublishedKGPayload{
				AtomID:   strField(raw, "atom_id"),
				TenantID: tenantID,
			})
		case events.TopicAtomRevisionUpdated:
			if atomRevisionUpdated == nil {
				return nil
			}
			return atomRevisionUpdated.Handle(tctx, env, AtomRevisionUpdatedKGPayload{
				AtomID:   strField(raw, "atom_id"),
				TenantID: tenantID,
			})
		case events.TopicUserRetentionShifted:
			if userRetentionShifted == nil {
				return nil
			}
			return userRetentionShifted.Handle(tctx, env, UserRetentionShiftedKGPayload{
				TenantID: tenantID,
				UserGCID: strField(raw, "user_gcid"),
			})
		}
		return nil
	}
}

// ---------------------------------------------------------------------------
// Wave-1 XP (F-I3 / CHO-2090) + proofing-test terminal (CHO-2040)
// ---------------------------------------------------------------------------

// WeaknessGrownExpHandler adapts the WaveOneXPSubscriber's weakness-grown leg
// to an eventbus.Handler.
func WeaknessGrownExpHandler(s *WaveOneXPSubscriber) eventbus.Handler {
	return func(ctx context.Context, msg eventbus.Message) error {
		if s == nil {
			return errors.New("subscribers: weakness_grown_exp handler not initialised")
		}
		env := projectEnvelope(msg.Envelope)
		p, err := decodeWeaknessGrownPayload(msg.Payload)
		if err != nil {
			return fmt.Errorf("weakness_grown_exp: decode payload: %w", err)
		}
		ctx = tracing.WithGCID(tracing.WithTenantID(ctx, env.TenantID), env.GCID)
		return s.HandleWeaknessGrown(ctx, env, p)
	}
}

// ModuleCompletedExpHandler adapts the WaveOneXPSubscriber's module-completed
// leg to an eventbus.Handler.
func ModuleCompletedExpHandler(s *WaveOneXPSubscriber) eventbus.Handler {
	return func(ctx context.Context, msg eventbus.Message) error {
		if s == nil {
			return errors.New("subscribers: module_completed_exp handler not initialised")
		}
		env := projectEnvelope(msg.Envelope)
		raw, err := protodecode.DecodePayloadMap(TopicModuleProgressCompleted, msg.Payload)
		if err != nil {
			return fmt.Errorf("module_completed_exp: %s: %w", TopicModuleProgressCompleted, err)
		}
		p := ModuleCompletedPayload{
			ModuleID:    strField(raw, "module_id"),
			CourseID:    strField(raw, "course_id"),
			LearnerGCID: strField(raw, "learner_gcid"),
			TenantID:    strField(raw, "tenant_id"),
		}
		if p.LearnerGCID == "" {
			p.LearnerGCID = strField(raw, "gcid") // envelope gcid IS the learner
		}
		ctx = tracing.WithGCID(tracing.WithTenantID(ctx, env.TenantID), env.GCID)
		return s.HandleModuleCompleted(ctx, env, p)
	}
}

const (
	TopicAiAssistCompletedV1 = "chora.creation.ai_assist.completed.v1"
	TopicAiAssistRefusedV1   = "chora.creation.ai_assist.refused.v1"
)

// ProofingTestTerminalHandler adapts the ProofingTestTerminalSubscriber (+ the
// campaign question-set bridge) to an eventbus.Handler.
func ProofingTestTerminalHandler(sub *ProofingTestTerminalSubscriber, campaign *CampaignQuestionTerminalSubscriber) eventbus.Handler {
	return func(ctx context.Context, msg eventbus.Message) error {
		if sub == nil {
			return errors.New("subscribers: proofing_test_terminal handler not initialised")
		}
		topic := msg.Subject
		if topic != TopicAiAssistCompletedV1 && topic != TopicAiAssistRefusedV1 {
			return nil
		}
		env := projectEnvelope(msg.Envelope)
		ctx = tracing.WithGCID(tracing.WithTenantID(ctx, env.TenantID), env.GCID)
		if len(msg.Payload) == 0 {
			return errors.New("proofing_test_terminal: empty payload")
		}
		switch topic {
		case TopicAiAssistCompletedV1:
			var m creationv1.AiAssistCompleted
			if err := proto.Unmarshal(msg.Payload, &m); err != nil {
				return fmt.Errorf("proofing_test_terminal: proto.Unmarshal AiAssistCompleted: %w", err)
			}
			p := ProofingCompletedPayload{
				AssistID:             m.GetAssistId(),
				CandidatePayloadJSON: m.GetCandidatePayloadJson(),
				QualityWarning:       m.GetQualityWarning(),
			}
			if err := sub.HandleCompleted(ctx, env, p); err != nil {
				return err
			}
			if campaign != nil {
				return campaign.HandleCompleted(ctx, env, p)
			}
			return nil
		default: // TopicAiAssistRefusedV1
			var m creationv1.AiAssistRefused
			if err := proto.Unmarshal(msg.Payload, &m); err != nil {
				return fmt.Errorf("proofing_test_terminal: proto.Unmarshal AiAssistRefused: %w", err)
			}
			p := ProofingRefusedPayload{
				AssistID:          m.GetAssistId(),
				RefusalReason:     m.GetRefusalReason(),
				UserFacingMessage: m.GetUserFacingMessage(),
			}
			if err := sub.HandleRefused(ctx, env, p); err != nil {
				return err
			}
			if campaign != nil {
				return campaign.HandleRefused(ctx, env, p)
			}
			return nil
		}
	}
}

// ---------------------------------------------------------------------------
// Weakness analyzed (Epic-1b) + review pending (ADR-205 D4)
// ---------------------------------------------------------------------------

// WeaknessAnalyzedHandler adapts the WeaknessAnalyzedSubscriber to an
// eventbus.Handler. The analyzed event is BINARY-only (no legacy JSON shape).
func WeaknessAnalyzedHandler(s *WeaknessAnalyzedSubscriber) eventbus.Handler {
	return func(ctx context.Context, msg eventbus.Message) error {
		if s == nil {
			return errors.New("subscribers: weakness_analyzed handler not initialised")
		}
		env := projectEnvelope(msg.Envelope)
		p, err := decodeWeaknessAnalyzedPayload(msg.Payload)
		if err != nil {
			return fmt.Errorf("weakness_analyzed: decode payload: %w", err)
		}
		// RLS: the subscriber's upsert re-scopes the ctx from the payload
		// tenant + gcid, but stamp the envelope tenant/gcid here on entry too.
		ctx = tracing.WithGCID(tracing.WithTenantID(ctx, env.TenantID), env.GCID)
		return s.Handle(ctx, env, p)
	}
}

// decodeWeaknessAnalyzedPayload proto.Unmarshals the binary
// chora.consumption.weakness.analyzed.v1 body into the subscriber's typed
// payload. Fails loud on empty / non-proto bytes (→ NACK).
func decodeWeaknessAnalyzedPayload(data []byte) (WeaknessAnalyzedPayload, error) {
	if len(data) == 0 {
		return WeaknessAnalyzedPayload{}, errors.New("empty payload")
	}
	var m consumptionv1.WeaknessAnalyzed
	if err := proto.Unmarshal(data, &m); err != nil {
		return WeaknessAnalyzedPayload{}, fmt.Errorf("proto.Unmarshal WeaknessAnalyzed: %w", err)
	}
	edges := make([]ExtractedGrowthEdgePayload, 0, len(m.GetEdges()))
	for _, e := range m.GetEdges() {
		edges = append(edges, ExtractedGrowthEdgePayload{
			ConceptLabel:   e.GetConceptLabel(),
			ConceptKey:     e.GetConceptKey(),
			Category:       e.GetCategory(),
			Tags:           e.GetTags(),
			Confidence:     float64(e.GetConfidence()),
			Strength:       float64(e.GetStrength()),
			DescriptorJSON: e.GetDescriptorJson(),
		})
	}
	return WeaknessAnalyzedPayload{
		UploadID:    m.GetUploadId(),
		TenantID:    m.GetTenantId(),
		LearnerGCID: m.GetLearnerGcid(),
		ModelUsed:   m.GetModelUsed(),
		Edges:       edges,
		// ADR-205 D5 (CHO-1966): the learner's post-HITL familiar_coaching opt-in
		// (nil-safe — absent OutputSelection ⇒ false ⇒ no Companion-RAG write).
		CompanionCoaching: m.GetOutputSelection().GetFamiliarCoaching(),
	}, nil
}

// WeaknessReviewPendingHandler adapts the WeaknessReviewPendingSubscriber to
// an eventbus.Handler.
func WeaknessReviewPendingHandler(s *WeaknessReviewPendingSubscriber) eventbus.Handler {
	return func(ctx context.Context, msg eventbus.Message) error {
		if s == nil {
			return errors.New("subscribers: weakness_review_pending handler not initialised")
		}
		env := projectEnvelope(msg.Envelope)
		p, err := decodeWeaknessReviewPendingPayload(msg.Payload)
		if err != nil {
			return fmt.Errorf("weakness_review_pending: decode payload: %w", err)
		}
		ctx = tracing.WithGCID(tracing.WithTenantID(ctx, env.TenantID), env.GCID)
		return s.Handle(ctx, env, p)
	}
}

func decodeWeaknessReviewPendingPayload(data []byte) (WeaknessReviewPendingPayload, error) {
	if len(data) == 0 {
		return WeaknessReviewPendingPayload{}, errors.New("empty payload")
	}
	var m consumptionv1.WeaknessReviewPending
	if err := proto.Unmarshal(data, &m); err != nil {
		return WeaknessReviewPendingPayload{}, fmt.Errorf("proto.Unmarshal WeaknessReviewPending: %w", err)
	}
	return WeaknessReviewPendingPayload{
		UploadID:    m.GetUploadId(),
		TenantID:    m.GetTenantId(),
		LearnerGCID: m.GetLearnerGcid(),
		Panel:       reviewPanelFromProto(&m),
		PendingAt:   m.GetPendingAt().AsTime(),
	}, nil
}

// reviewPanelFromProto projects the proto review panel onto the domain shape.
// Slices are always non-nil (marshal as [] not null) so the A+ poll always
// sees an array. proposed_edge_id is copied verbatim (orchestrator-owned,
// OPAQUE).
func reviewPanelFromProto(m *consumptionv1.WeaknessReviewPending) *wu.ReviewPanel {
	panel := &wu.ReviewPanel{
		ProposedEdges:      make([]wu.ProposedEdge, 0, len(m.GetProposedEdges())),
		CandidateStruggles: make([]wu.CandidateStruggle, 0, len(m.GetCandidateStruggles())),
		AvailableOutputs:   make([]wu.AvailableOutput, 0, len(m.GetAvailableOutputs())),
	}
	// Wire side is ReviewFamiliar/familiar_id (WIRE-FROZEN on the registered v1
	// schema, ADR-254); the domain side is ReviewCompanion/CompanionID.
	if f := m.GetFamiliar(); f != nil {
		panel.Companion = &wu.ReviewCompanion{
			CompanionID: f.GetFamiliarId(),
			Name:        f.GetName(),
			Species:     f.GetSpecies(),
		}
	}
	for _, e := range m.GetProposedEdges() {
		panel.ProposedEdges = append(panel.ProposedEdges, wu.ProposedEdge{
			ProposedEdgeID:      e.GetProposedEdgeId(),
			ConceptLabel:        e.GetConceptLabel(),
			Summary:             e.GetSummary(),
			SuggestedAngles:     e.GetSuggestedAngles(),
			Strength:            e.GetStrength(),
			SuggestedDifficulty: e.GetSuggestedDifficulty(),
		})
	}
	for _, c := range m.GetCandidateStruggles() {
		panel.CandidateStruggles = append(panel.CandidateStruggles, wu.CandidateStruggle{
			ConceptKey:   c.GetConceptKey(),
			ConceptLabel: c.GetConceptLabel(),
		})
	}
	for _, o := range m.GetAvailableOutputs() {
		panel.AvailableOutputs = append(panel.AvailableOutputs, wu.AvailableOutput{
			Kind:            o.GetKind(),
			ManaPrice:       o.GetManaPrice(),
			DefaultSelected: o.GetDefaultSelected(),
		})
	}
	return panel
}
