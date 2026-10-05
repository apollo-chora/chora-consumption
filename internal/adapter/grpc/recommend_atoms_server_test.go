// recommend_atoms_server_test.go — TDD for the Consumption gRPC
// RecommendAtomsForLearner RPC. This RPC is the sanctioned RAG-retrieval
// surface the AI Kernel Content Recommender crew calls to replace its
// atom-stub source with REAL published atoms from the local atom_index
// projection (cross-DB FORBIDDEN — the crew dials chora-consumption:9090).
package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
	consumptionv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/consumption/v1"
)

// fakeAtomIndexSearcher captures the SearchForLearner inputs + returns canned
// projections so the server mapping is verified without a DB.
type fakeAtomIndexSearcher struct {
	gotTenant string
	gotTopic  string
	gotLimit  int
	ret       []*atom_index.AtomIndex
	err       error
}

func (f *fakeAtomIndexSearcher) SearchForLearner(_ context.Context, tenantID, topicHint string, limit int) ([]*atom_index.AtomIndex, error) {
	f.gotTenant = tenantID
	f.gotTopic = topicHint
	f.gotLimit = limit
	return f.ret, f.err
}

func TestRecommendAtomsForLearner_MapsProjectionsToProto(t *testing.T) {
	pub := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	a1, _ := atom_index.New(atom_index.NewParams{
		AtomID: "atom-1", TenantID: "t1", Title: "Agile Basics", AtomType: "mcq",
		Difficulty: 3, TopicTags: []string{"agile", "scrum"}, PublishedAt: pub,
	})
	fake := &fakeAtomIndexSearcher{ret: []*atom_index.AtomIndex{a1}}
	srv := NewConsumptionServer(fake)

	resp, err := srv.RecommendAtomsForLearner(context.Background(), &consumptionv1.RecommendAtomsForLearnerRequest{
		TenantId:    "t1",
		LearnerGcid: "g1",
		TopicHint:   "agile",
		Limit:       3,
	})
	require.NoError(t, err)
	require.Len(t, resp.GetRecommendations(), 1)

	got := resp.GetRecommendations()[0]
	assert.Equal(t, "atom-1", got.GetAtomId())
	assert.Equal(t, "Agile Basics", got.GetTitle())
	assert.Equal(t, "agile", got.GetTopic(), "topic = first topic tag")
	assert.Equal(t, int32(3), got.GetDifficulty())
	assert.Equal(t, pub.Unix(), got.GetPublishedAt().AsTime().Unix())

	// Inputs propagated verbatim to the repo (tenant scoping + topic bias).
	assert.Equal(t, "t1", fake.gotTenant)
	assert.Equal(t, "agile", fake.gotTopic)
	assert.Equal(t, 3, fake.gotLimit)
}

func TestRecommendAtomsForLearner_RejectsMissingTenant(t *testing.T) {
	srv := NewConsumptionServer(&fakeAtomIndexSearcher{})
	_, err := srv.RecommendAtomsForLearner(context.Background(), &consumptionv1.RecommendAtomsForLearnerRequest{
		LearnerGcid: "g1",
	})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestRecommendAtomsForLearner_RejectsMissingGCID(t *testing.T) {
	srv := NewConsumptionServer(&fakeAtomIndexSearcher{})
	_, err := srv.RecommendAtomsForLearner(context.Background(), &consumptionv1.RecommendAtomsForLearnerRequest{
		TenantId: "t1",
	})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestRecommendAtomsForLearner_UnwiredRepoIsUnimplemented(t *testing.T) {
	// A server constructed without a searcher must fail-loud, not return a
	// silent empty list (feedback_no_stubs_real_wiring).
	srv := NewConsumptionServer(nil)
	_, err := srv.RecommendAtomsForLearner(context.Background(), &consumptionv1.RecommendAtomsForLearnerRequest{
		TenantId: "t1", LearnerGcid: "g1",
	})
	require.Error(t, err)
	assert.Equal(t, codes.Unimplemented, status.Code(err))
}

func TestRecommendAtomsForLearner_EmptyResultIsOK(t *testing.T) {
	srv := NewConsumptionServer(&fakeAtomIndexSearcher{ret: nil})
	resp, err := srv.RecommendAtomsForLearner(context.Background(), &consumptionv1.RecommendAtomsForLearnerRequest{
		TenantId: "t1", LearnerGcid: "g1",
	})
	require.NoError(t, err)
	assert.Empty(t, resp.GetRecommendations())
}
