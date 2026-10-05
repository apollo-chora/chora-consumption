// subscriber_wiring.go — cmd/server boot helper binding the cross-domain
// subscribers to the eventbus (the NATS pull subscriptions that replaced the
// retired Pub/Sub push HTTP inboxes).
//
// Two subscribers were declared in `internal/adapter/subscribers/subscribers.go`
// and constructed inside `NewExtServer` since S4.2 + S5.2 but had no
// production binding — they were dormant. This file closes that gap by
// binding each to its event subject so the existing subscriber code path
// fires on real deliveries:
//
//   - EnrollmentCreatedSub bootstraps Straight-Up LearningPath rows from
//     chora.delivery.enrollment.created.v1 — without it, the open-course
//     /v1/me/learning-paths?course_id=X read returns 404 forever.
//   - AtomCreatedSub upserts the local atom_index projection from
//     chora.creation.atom.created.v1 — without it, atom_index stays empty,
//     server-side MCQ grading falls through to non-gradable, and
//     LearningPath bootstrap defers on empty atom lists.
//
// Per the always-loaded ddd-enforcement rule the subscribers are inbound
// adapters — this helper never touches domain logic. When the bus is absent
// (NATS_URL unset) the subscribers stay constructed but unbound, mirroring
// the chora-identity pattern.
package main

import (
	"github.com/apollo-chora/chora-consumption/internal/adapter/subscribers"
	"context"
	"errors"
	"log"

	"github.com/apollo-chora/chora-common/eventbus"

	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
)

// wireSubscriberBusBindings binds the ExtServer's cross-domain subscribers to
// the eventbus. Each binding is a durable pull subscription (see
// consumerConfig). When the bus is nil (NATS_URL unset) nothing is bound and
// the subscribers stay constructed-but-inert, exactly like chora-identity.
func wireSubscriberBusBindings(ctx context.Context, srv *httpadapter.Server, ext *httpadapter.ExtServer, bus eventbus.Bus) {
	if srv == nil || ext == nil {
		return
	}
	if bus == nil {
		log.Printf("consumption: cross-domain subscribers NOT bound (NATS_URL unset): enrollment-created / atom-created / atom-published / course-* / learner-profile / learning-path-topics stay inert")
		return
	}
	bind := func(name, subject string, handler eventbus.Handler) {
		go func() {
			log.Printf("consumption: subscriber binding %s -> %s", name, subject)
			if err := bus.Subscribe(ctx, consumerConfig(name, subject), handler); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("ERROR consumption: subscriber %s exited: %v", name, err)
			}
		}()
	}

	if ext.EnrollmentCreatedSub != nil {
		bind("chora-consumption.enrollment-created", subscribers.TopicDeliveryEnrollmentCreated, subscribers.EnrollmentCreatedHandler(ext.EnrollmentCreatedSub))
		log.Printf("consumption: eventbus binding: chora.delivery.enrollment.created.v1 → LearningPath bootstrap")
	}
	if ext.AtomCreatedSub != nil {
		bind("chora-consumption.atom-created", subscribers.TopicCreationAtomCreated, subscribers.AtomCreatedHandler(ext.AtomCreatedSub))
		log.Printf("consumption: eventbus binding: chora.creation.atom.created.v1 → atom_index projection")
	}
	if ext.AtomPublishedSub != nil {
		bind("chora-consumption.atom-published", subscribers.TopicCreationAtomPublished, subscribers.AtomPublishedHandler(ext.AtomPublishedSub, ext.AtomRefreshSub))
		log.Printf("consumption: eventbus binding: chora.creation.atom.published.v1 → atom_index playability flip + ADR-244 D5 refresh fan-out")
	}
	if ext.AtomRefreshSub != nil {
		bind("chora-consumption.atom-updated", subscribers.TopicCreationAtomUpdated, subscribers.AtomUpdatedHandler(ext.AtomRefreshSub))
		log.Printf("consumption: eventbus binding: chora.creation.atom.updated.v1 → ADR-244 D5 refresh trigger")
	}
	// CHO-2167 (closes CHO-1471) — the dispatch this subscriber never had. Both
	// learning_path topics land on ONE inbox → active_path_topics → the daily
	// dose's CURIOSITY slot. Without this the subscriber is constructed and
	// never called, which is precisely how the projection reached production
	// with zero rows.
	if ext.ActivePathTopicsSub != nil {
		bind("chora-consumption.learning-path-topics", subscribers.TopicLearningPathBootstrapped, subscribers.LearningPathTopicsHandler(ext.ActivePathTopicsSub))
		log.Printf("consumption: eventbus binding: chora.consumption.learning_path.{bootstrapped,advanced}.v1 → active_path_topics → dose CURIOSITY slot")
	}
	if ext.CourseContentSub != nil {
		bind("chora-consumption.course-content-composed", subscribers.TopicDeliveryCourseContentComposed, subscribers.CourseContentComposedHandler(ext.CourseContentSub))
		log.Printf("consumption: eventbus binding: chora.delivery.course.content_composed.v1 → course-content projection")
	}
	if ext.CourseMetadataSub != nil {
		bind("chora-consumption.course-metadata", subscribers.TopicDeliveryCourseCreated, subscribers.CourseMetadataHandler(ext.CourseMetadataSub))
		log.Printf("consumption: eventbus binding: chora.delivery.course.{created,released}.v1 → course_directory projection (CHO-2059)")
	}
	if ext.LearnerProfileSub != nil {
		lp := ext.LearnerProfileSub
		transcript := ext.TranscriptSub
		bind("chora-consumption.learner-profile-cert-issued", "chora.delivery.certification.issued.v1", subscribers.LearnerProfileHandler(lp, transcript, "chora.delivery.certification.issued.v1"))
		bind("chora-consumption.learner-profile-path-completed", "chora.consumption.learning_path.completed.v1", subscribers.LearnerProfileHandler(lp, nil, "chora.consumption.learning_path.completed.v1"))
		bind("chora-consumption.learner-profile-enrollment-created", subscribers.TopicDeliveryEnrollmentCreated, subscribers.LearnerProfileHandler(lp, nil, subscribers.TopicDeliveryEnrollmentCreated))
		bind("chora-consumption.learner-profile-submission-graded", subscribers.TopicSubmissionGraded, subscribers.LearnerProfileHandler(lp, transcript, subscribers.TopicSubmissionGraded))
		bind("chora-consumption.learner-profile-enrollment-completed", "chora.delivery.enrollment.completed.v1", subscribers.LearnerProfileHandler(lp, nil, "chora.delivery.enrollment.completed.v1"))
		bind("chora-consumption.learner-profile-preferences-updated", "chora.consumption.preferences.updated.v1", subscribers.LearnerProfileHandler(lp, nil, "chora.consumption.preferences.updated.v1"))
		transcriptNote := "NOT wired (no pg pool)"
		if ext.TranscriptSub != nil {
			transcriptNote = "wired"
		}
		log.Printf("consumption: eventbus binding: LearnerProfile projection (cert-issued / path-completed / enrollment-created / submission-graded / enrollment-completed / preferences-updated → ADR-200 read-model); StudentTranscript fan-out %s (W6 Slice 1)", transcriptNote)
	}
}
