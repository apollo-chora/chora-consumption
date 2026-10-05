package atomrefresh_test

import (
	"strings"
	"testing"
	"time"

	atomrefresh "github.com/apollo-chora/chora-consumption/internal/domain/atom_refresh"
)

func TestNewRefreshClaim_MintsUUIDv7AndCarriesIdentity(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	c, err := atomrefresh.NewRefreshClaim("tenant-1", "learner-1", "atom-1", "focal-1", "event-1", now)
	if err != nil {
		t.Fatalf("NewRefreshClaim: %v", err)
	}
	if c.ID == "" {
		t.Fatalf("expected a minted id")
	}
	if c.TenantID != "tenant-1" || c.LearnerGCID != "learner-1" || c.AtomID != "atom-1" ||
		c.FocalConceptID != "focal-1" || c.TriggerEventID != "event-1" {
		t.Fatalf("identity fields not carried: %+v", c)
	}
	if c.RequestID != nil || c.PublishedAt != nil {
		t.Fatalf("fresh claim must be unpublished: %+v", c)
	}
	if !c.CreatedAt.Equal(now) {
		t.Fatalf("CreatedAt = %v, want %v", c.CreatedAt, now)
	}
}

func TestNewRefreshClaim_RejectsMissingFields(t *testing.T) {
	now := time.Now().UTC()
	cases := [][5]string{
		{"", "l", "a", "f", "e"},
		{"t", "", "a", "f", "e"},
		{"t", "l", "", "f", "e"},
		{"t", "l", "a", "", "e"},
		{"t", "l", "a", "f", ""},
	}
	for i, in := range cases {
		if _, err := atomrefresh.NewRefreshClaim(in[0], in[1], in[2], in[3], in[4], now); err == nil {
			t.Fatalf("case %d: expected error for missing field", i)
		}
	}
}

func TestCaps_AreBoundedAndPositive(t *testing.T) {
	if atomrefresh.MaxProposalsPerAtomEvent < 1 || atomrefresh.MaxProposalsPerAtomEvent > 10 {
		t.Fatalf("MaxProposalsPerAtomEvent out of sane bounds: %d", atomrefresh.MaxProposalsPerAtomEvent)
	}
	if atomrefresh.MaxProposalsPerLearnerPerDay < 1 || atomrefresh.MaxProposalsPerLearnerPerDay > 50 {
		t.Fatalf("MaxProposalsPerLearnerPerDay out of sane bounds: %d", atomrefresh.MaxProposalsPerLearnerPerDay)
	}
}

func TestErrRefreshClaimNotFound_Message(t *testing.T) {
	if !strings.Contains(atomrefresh.ErrRefreshClaimNotFound.Error(), "atom_refresh") {
		t.Fatalf("error should name the package: %v", atomrefresh.ErrRefreshClaimNotFound)
	}
}
