// grader_test.go — WS-C1 (CHO-2080) contract for the campaign grading
// service: one server-graded answer in → ladder fold + retention
// plant/review (keyed by the node's CONCEPT_KEY, addendum #2) + verified
// events out, in persist-before-emit order.
package campaign

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/topic_retention"
)

// --- fakes -------------------------------------------------------------------

type fakeProgressRepo struct {
	rows    map[string]*NodeProgress // key = conceptID
	saveErr error
	saved   int
}

func (f *fakeProgressRepo) GetByConcept(_ context.Context, _, _, conceptID string) (*NodeProgress, error) {
	return f.rows[conceptID], nil
}
func (f *fakeProgressRepo) ListByLearner(context.Context, string, string) ([]*NodeProgress, error) {
	out := []*NodeProgress{}
	for _, p := range f.rows {
		out = append(out, p)
	}
	return out, nil
}
func (f *fakeProgressRepo) Save(_ context.Context, p *NodeProgress) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	if f.rows == nil {
		f.rows = map[string]*NodeProgress{}
	}
	f.rows[p.ConceptID] = p
	f.saved++
	return nil
}

type fakeRetentionRepo struct {
	rows map[string]*topic_retention.TopicScore // key = topicID (concept_key)
}

func (f *fakeRetentionRepo) Save(_ context.Context, s *topic_retention.TopicScore) error {
	if f.rows == nil {
		f.rows = map[string]*topic_retention.TopicScore{}
	}
	f.rows[s.TopicID] = s
	return nil
}
func (f *fakeRetentionRepo) Get(_ context.Context, _, _, topicID string) (*topic_retention.TopicScore, error) {
	return f.rows[topicID], nil
}
func (f *fakeRetentionRepo) ListByLearner(context.Context, string, string, int) ([]*topic_retention.TopicScore, error) {
	return nil, nil
}

type fakeEventSink struct {
	rungCleared []RungClearedEvent
	nodeWon     []NodeWonEvent
}

func (f *fakeEventSink) CampaignRungCleared(_ context.Context, e RungClearedEvent) error {
	f.rungCleared = append(f.rungCleared, e)
	return nil
}
func (f *fakeEventSink) CampaignNodeWon(_ context.Context, e NodeWonEvent) error {
	f.nodeWon = append(f.nodeWon, e)
	return nil
}

func newTestGrader(t *testing.T, prog *fakeProgressRepo, ret *fakeRetentionRepo, sink *fakeEventSink) *Grader {
	t.Helper()
	g, err := NewGrader(GraderConfig{Progress: prog, Retention: ret, Events: sink})
	if err != nil {
		t.Fatalf("NewGrader: %v", err)
	}
	return g
}

func answerIn(rung Rung, correct bool, at time.Time) RecordAnswerInput {
	return RecordAnswerInput{
		TenantID: cTenant, LearnerGCID: cGCID, GoalID: "01971a00-0000-7000-8000-00000000000e",
		ConceptID: cConcept, ConceptKey: "photosynthesis",
		Rung: rung, Correct: correct, Now: at,
	}
}

// --- tests --------------------------------------------------------------------

func TestGrader_FirstAnswerPlantsLadderAndRetention(t *testing.T) {
	prog := &fakeProgressRepo{}
	ret := &fakeRetentionRepo{}
	sink := &fakeEventSink{}
	g := newTestGrader(t, prog, ret, sink)

	res, err := g.RecordAnswer(context.Background(), answerIn(RungKnowledge, true, cNow))
	if err != nil {
		t.Fatalf("RecordAnswer: %v", err)
	}
	if !res.Outcome.Counted || res.Outcome.ClearedRung != 0 {
		t.Errorf("outcome = %+v, want counted-only first correct", res.Outcome)
	}
	if prog.rows[cConcept] == nil || prog.rows[cConcept].CurrentRungCorrect != 1 {
		t.Errorf("ladder row not lazily created/saved: %+v", prog.rows[cConcept])
	}
	score := ret.rows["photosynthesis"]
	if score == nil {
		t.Fatal("retention row not PLANTED under the concept_key (addendum #2)")
	}
	if score.ReviewCount != 1 {
		t.Errorf("retention not reviewed: %+v", score)
	}
	if res.RetentionR <= 0 || res.RetentionR > 1 {
		t.Errorf("RetentionR = %v", res.RetentionR)
	}
	if len(sink.rungCleared) != 0 || len(sink.nodeWon) != 0 {
		t.Errorf("no events expected on a non-clearing correct: %+v %+v", sink.rungCleared, sink.nodeWon)
	}
}

func TestGrader_ClearEmitsRungCleared(t *testing.T) {
	prog := &fakeProgressRepo{}
	ret := &fakeRetentionRepo{}
	sink := &fakeEventSink{}
	g := newTestGrader(t, prog, ret, sink)

	if _, err := g.RecordAnswer(context.Background(), answerIn(RungKnowledge, true, cNow)); err != nil {
		t.Fatalf("correct #1: %v", err)
	}
	res, err := g.RecordAnswer(context.Background(), answerIn(RungKnowledge, true, cNow.Add(time.Minute)))
	if err != nil {
		t.Fatalf("correct #2: %v", err)
	}
	if res.Outcome.ClearedRung != RungKnowledge {
		t.Fatalf("outcome = %+v, want knowledge cleared", res.Outcome)
	}
	if len(sink.rungCleared) != 1 {
		t.Fatalf("rungCleared events = %d, want 1", len(sink.rungCleared))
	}
	e := sink.rungCleared[0]
	if e.Rung != RungKnowledge || e.IsRefresher || e.CorrectAnswers != DefaultRungClearCorrect || e.GoalID == "" || e.ConceptKey != "photosynthesis" {
		t.Errorf("event = %+v", e)
	}
	if len(sink.nodeWon) != 0 {
		t.Errorf("nodeWon must not fire at rung 1: %+v", sink.nodeWon)
	}
}

func TestGrader_RefresherCorrectEmitsReducedEvent(t *testing.T) {
	prog := &fakeProgressRepo{}
	ret := &fakeRetentionRepo{}
	sink := &fakeEventSink{}
	g := newTestGrader(t, prog, ret, sink)

	// Clear rung 1 (2 corrects), then answer rung 1 again (a D8 defence).
	if _, err := g.RecordAnswer(context.Background(), answerIn(RungKnowledge, true, cNow)); err != nil {
		t.Fatal(err)
	}
	if _, err := g.RecordAnswer(context.Background(), answerIn(RungKnowledge, true, cNow.Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	res, err := g.RecordAnswer(context.Background(), answerIn(RungKnowledge, true, cNow.Add(2*time.Hour)))
	if err != nil {
		t.Fatalf("refresher: %v", err)
	}
	if !res.Outcome.IsRefresher {
		t.Fatalf("outcome = %+v, want refresher", res.Outcome)
	}
	if len(sink.rungCleared) != 2 {
		t.Fatalf("events = %d, want clear + refresher", len(sink.rungCleared))
	}
	if e := sink.rungCleared[1]; !e.IsRefresher || e.CorrectAnswers != 1 {
		t.Errorf("refresher event = %+v", e)
	}
	// A WRONG refresher reviews retention but emits nothing.
	before := len(sink.rungCleared)
	if _, err := g.RecordAnswer(context.Background(), answerIn(RungKnowledge, false, cNow.Add(3*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if len(sink.rungCleared) != before {
		t.Error("wrong refresher must not emit")
	}
	if ret.rows["photosynthesis"].ReviewCount != 4 {
		t.Errorf("every graded answer reviews retention; count = %d", ret.rows["photosynthesis"].ReviewCount)
	}
}

func TestGrader_WinEmitsNodeWon(t *testing.T) {
	prog := &fakeProgressRepo{}
	ret := &fakeRetentionRepo{}
	sink := &fakeEventSink{}
	g := newTestGrader(t, prog, ret, sink)

	day := cNow
	for _, r := range []Rung{RungKnowledge, RungComprehension, RungApplication, RungAnalysis, RungEvaluation, RungSynthesis} {
		for i := 0; i < DefaultRungClearCorrect; i++ {
			if _, err := g.RecordAnswer(context.Background(), answerIn(r, true, day.Add(time.Duration(i)*time.Minute))); err != nil {
				t.Fatalf("rung %d correct %d: %v", r, i+1, err)
			}
		}
		day = day.Add(24 * time.Hour)
	}
	if len(sink.nodeWon) != 1 {
		t.Fatalf("nodeWon events = %d, want exactly 1", len(sink.nodeWon))
	}
	if e := sink.nodeWon[0]; e.ConceptID != cConcept || e.GoalID == "" || e.WonAt.IsZero() {
		t.Errorf("nodeWon event = %+v", e)
	}
	if len(sink.rungCleared) != 6 {
		t.Errorf("rungCleared events = %d, want 6", len(sink.rungCleared))
	}
	// Post-win grading refuses (D9) — no further events or reviews.
	if _, err := g.RecordAnswer(context.Background(), answerIn(RungSynthesis, true, day)); !errors.Is(err, ErrAlreadyWon) {
		t.Errorf("post-win err = %v, want ErrAlreadyWon", err)
	}
}

func TestGrader_SaveFailureEmitsNothing(t *testing.T) {
	boom := errors.New("db down")
	prog := &fakeProgressRepo{saveErr: boom}
	ret := &fakeRetentionRepo{}
	sink := &fakeEventSink{}
	g := newTestGrader(t, prog, ret, sink)

	if _, err := g.RecordAnswer(context.Background(), answerIn(RungKnowledge, true, cNow)); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want save failure", err)
	}
	if len(sink.rungCleared)+len(sink.nodeWon) != 0 {
		t.Error("persist-before-emit violated: events fired despite save failure")
	}
}

func TestGrader_ServeDecisionFor(t *testing.T) {
	prog := &fakeProgressRepo{}
	ret := &fakeRetentionRepo{}
	sink := &fakeEventSink{}
	g := newTestGrader(t, prog, ret, sink)

	// Unstarted node, no retention → rung 1.
	dec, err := g.ServeDecisionFor(context.Background(), cTenant, cGCID, cConcept, "photosynthesis", cNow)
	if err != nil {
		t.Fatalf("ServeDecisionFor: %v", err)
	}
	if dec.Rung != RungKnowledge || dec.IsRefresher {
		t.Errorf("decision = %+v", dec)
	}

	// Clear rung 1 today; immediately after, retention is warm → next rung.
	if _, err := g.RecordAnswer(context.Background(), answerIn(RungKnowledge, true, cNow)); err != nil {
		t.Fatal(err)
	}
	if _, err := g.RecordAnswer(context.Background(), answerIn(RungKnowledge, true, cNow.Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	dec, err = g.ServeDecisionFor(context.Background(), cTenant, cGCID, cConcept, "photosynthesis", cNow.Add(time.Hour))
	if err != nil {
		t.Fatalf("ServeDecisionFor: %v", err)
	}
	if dec.Rung != RungComprehension || dec.IsRefresher {
		t.Errorf("warm decision = %+v, want next rung", dec)
	}

	// Ten days later the row has gone cold (seed strength 1d) → refresher at
	// the highest cleared rung (D8).
	dec, err = g.ServeDecisionFor(context.Background(), cTenant, cGCID, cConcept, "photosynthesis", cNow.Add(10*24*time.Hour))
	if err != nil {
		t.Fatalf("ServeDecisionFor: %v", err)
	}
	if dec.Rung != RungKnowledge || !dec.IsRefresher {
		t.Errorf("cold decision = %+v, want refresher at highest cleared", dec)
	}
}

func TestNewGrader_Validates(t *testing.T) {
	if _, err := NewGrader(GraderConfig{}); !errors.Is(err, ErrInvalid) {
		t.Errorf("empty config err = %v, want ErrInvalid", err)
	}
	if _, err := NewGrader(GraderConfig{Progress: &fakeProgressRepo{}, Retention: &fakeRetentionRepo{}, Events: &fakeEventSink{}, RungClearCorrect: -1}); !errors.Is(err, ErrInvalid) {
		t.Errorf("negative threshold err = %v, want ErrInvalid", err)
	}
}

// The answers door reports needed_correct from the resolved threshold, so the
// grader exposes it (0 in config → the DefaultRungClearCorrect it resolves at
// construction; an explicit override reflected verbatim).
func TestGrader_RungClearCorrect_ExposesResolvedThreshold(t *testing.T) {
	g := newTestGrader(t, &fakeProgressRepo{}, &fakeRetentionRepo{}, &fakeEventSink{})
	if g.RungClearCorrect() != DefaultRungClearCorrect {
		t.Errorf("default RungClearCorrect() = %d, want %d", g.RungClearCorrect(), DefaultRungClearCorrect)
	}
	custom, err := NewGrader(GraderConfig{Progress: &fakeProgressRepo{}, Retention: &fakeRetentionRepo{}, Events: &fakeEventSink{}, RungClearCorrect: 3})
	if err != nil {
		t.Fatalf("NewGrader: %v", err)
	}
	if custom.RungClearCorrect() != 3 {
		t.Errorf("custom RungClearCorrect() = %d, want 3", custom.RungClearCorrect())
	}
}
