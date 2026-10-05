package events_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
)

// goodEnvelope is a fully-valid envelope used as the baseline; each test
// mutates exactly one field to drive a single validateEnvelope branch.
func goodEnvelope() events.Envelope {
	now := time.Now().UTC()
	return events.Envelope{
		EventID:        "01970000-0000-7000-8000-000000000001",
		IdempotencyKey: "01970000-0000-7000-8000-000000000001",
		TenantID:       "tenant-1",
		GCID:           "gcid-1",
		OccurredAt:     now,
		PublishedAt:    now,
		Traceparent:    "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		Tracestate:     "v=1",
		SourceProject:  events.SourceProject,
		SourceService:  events.SourceService,
		SchemaVersion:  1,
	}
}

// TestValidateEnvelope_EachMandatoryFieldBranch drives every individual
// mandatory-field rejection in validateEnvelope (via InMemoryPublisher.Publish)
// and asserts the error message names the offending field. Each row catches a
// regression where a specific mandatory-field guard is dropped.
func TestValidateEnvelope_EachMandatoryFieldBranch(t *testing.T) {
	cases := map[string]struct {
		mutate  func(e *events.Envelope)
		wantSub string
	}{
		"missing_event_id":        {func(e *events.Envelope) { e.EventID = "" }, "event_id"},
		"missing_idempotency_key": {func(e *events.Envelope) { e.IdempotencyKey = "" }, "idempotency_key"},
		"missing_tenant_id":       {func(e *events.Envelope) { e.TenantID = "" }, "tenant_id"},
		"zero_occurred_at":        {func(e *events.Envelope) { e.OccurredAt = time.Time{} }, "occurred_at"},
		"zero_published_at":       {func(e *events.Envelope) { e.PublishedAt = time.Time{} }, "published_at"},
		"missing_source_project":  {func(e *events.Envelope) { e.SourceProject = "" }, "source_project"},
		"missing_source_service":  {func(e *events.Envelope) { e.SourceService = "" }, "source_service"},
		"schema_version_zero":     {func(e *events.Envelope) { e.SchemaVersion = 0 }, "schema_version"},
		"missing_traceparent":     {func(e *events.Envelope) { e.Traceparent = "" }, "traceparent"},
	}

	for name, c := range cases {
		c := c
		t.Run(name, func(t *testing.T) {
			p := events.NewInMemoryPublisher()
			env := goodEnvelope()
			c.mutate(&env)
			err := p.Publish(events.TopicAtomSessionCompleted, env, nil)
			require.Error(t, err, "expected rejection")
			assert.Contains(t, err.Error(), c.wantSub)
			assert.Empty(t, p.Events(), "no event must be buffered on validation failure")
		})
	}
}

// TestValidateEnvelope_GCIDMayBeEmpty characterises the documented exception:
// gcid MAY be empty for system-emitted events, so a blank GCID with all other
// mandatory fields present must publish successfully.
func TestValidateEnvelope_GCIDMayBeEmpty(t *testing.T) {
	p := events.NewInMemoryPublisher()
	env := goodEnvelope()
	env.GCID = ""
	require.NoError(t, p.Publish(events.TopicAtomSessionCompleted, env, nil))
	require.Len(t, p.Events(), 1)
}

// TestValidateEnvelope_TracestateOptional asserts the W3C-optional tracestate
// can be empty while traceparent is present (the only trace field enforced).
func TestValidateEnvelope_TracestateOptional(t *testing.T) {
	p := events.NewInMemoryPublisher()
	env := goodEnvelope()
	env.Tracestate = ""
	require.NoError(t, p.Publish(events.TopicAtomSessionCompleted, env, nil))
	require.Len(t, p.Events(), 1)
}

// TestInMemoryClosureRepo_PseudonymiseIdempotent asserts the test double's
// Pseudonymise is idempotent: a repeat call for an already-processed GCID
// reports zero rows touched (and does not re-run the per-column count).
func TestInMemoryClosureRepo_PseudonymiseIdempotent(t *testing.T) {
	repo := events.NewInMemoryClosureRepo()
	ctx := context.Background()

	n1, err := repo.Pseudonymise(ctx, "tenant-1", "gcid-1", nil)
	require.NoError(t, err)
	assert.Equal(t, 0, n1, "nil spec → zero columns")
	pseudonymised, err := repo.IsPseudonymised(ctx, "tenant-1", "gcid-1")
	require.NoError(t, err)
	assert.True(t, pseudonymised)

	// Second call for the same gcid → already pseudonymised → 0 rows, no error.
	n2, err := repo.Pseudonymise(ctx, "tenant-1", "gcid-1", nil)
	require.NoError(t, err)
	assert.Equal(t, 0, n2)
}
