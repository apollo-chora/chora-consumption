// junction_test.go — RED-phase tests for the persistent Junction
// aggregate. ADR-143 §6 says JunctionOpportunity is transient (computed
// during fog-gen). The persistent Junction aggregate captures detection
// outcomes that need user-consented accept/reject lifecycle (per the
// agent-a S5.2 mission deliverables): a junction has its own ID and
// status (pending → accepted | rejected). Accepting triggers the
// MergeInto cluster operation; rejecting marks it inert so it stops
// being re-surfaced for the same pair.
package userknowledgegraph

import (
	"testing"
	"time"
)

func TestNewJunction_Success(t *testing.T) {
	now := time.Now().UTC()
	j, err := NewJunction(testTenantID, testUserGCID, "cluster-a", "cluster-b", []string{testAtomID, testAtomIDAlt}, now)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if j.JunctionID == "" {
		t.Error("expected JunctionID to be set")
	}
	if j.Status != JunctionStatusPending {
		t.Errorf("Status = %q, want pending", j.Status)
	}
	if len(j.OverlapAtomIDs) != 2 {
		t.Errorf("len OverlapAtomIDs = %d, want 2", len(j.OverlapAtomIDs))
	}
}

func TestNewJunction_RejectsSelfMerge(t *testing.T) {
	now := time.Now().UTC()
	_, err := NewJunction(testTenantID, testUserGCID, "same-cluster", "same-cluster", []string{testAtomID}, now)
	if err == nil {
		t.Error("expected error: ClusterA == ClusterB")
	}
}

func TestNewJunction_RejectsEmptyOverlap(t *testing.T) {
	now := time.Now().UTC()
	_, err := NewJunction(testTenantID, testUserGCID, "ca", "cb", []string{}, now)
	if err == nil {
		t.Error("expected error: empty overlap")
	}
}

func TestNewJunction_RejectsMissingFields(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name     string
		tenantID string
		userGCID string
		ca, cb   string
		overlap  []string
	}{
		{"empty tenant", "", testUserGCID, "ca", "cb", []string{testAtomID}},
		{"empty user", testTenantID, "", "ca", "cb", []string{testAtomID}},
		{"empty cluster A", testTenantID, testUserGCID, "", "cb", []string{testAtomID}},
		{"empty cluster B", testTenantID, testUserGCID, "ca", "", []string{testAtomID}},
	}
	for _, tc := range cases {
		_, err := NewJunction(tc.tenantID, tc.userGCID, tc.ca, tc.cb, tc.overlap, now)
		if err == nil {
			t.Errorf("%s: expected error", tc.name)
		}
	}
}

func TestJunction_Accept(t *testing.T) {
	now := time.Now().UTC()
	j, _ := NewJunction(testTenantID, testUserGCID, "ca", "cb", []string{testAtomID}, now)
	if err := j.Accept(testAtomID, now.Add(time.Minute)); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if j.Status != JunctionStatusAccepted {
		t.Errorf("Status = %q", j.Status)
	}
	if j.ResolvedViaAtomID != testAtomID {
		t.Errorf("ResolvedViaAtomID = %q", j.ResolvedViaAtomID)
	}
	if j.ResolvedAt == nil {
		t.Error("ResolvedAt nil")
	}
}

func TestJunction_AcceptRejectsDoubleAccept(t *testing.T) {
	now := time.Now().UTC()
	j, _ := NewJunction(testTenantID, testUserGCID, "ca", "cb", []string{testAtomID}, now)
	_ = j.Accept(testAtomID, now)
	if err := j.Accept(testAtomID, now); err == nil {
		t.Error("expected error on double-accept")
	}
}

func TestJunction_AcceptRejectsBadAtom(t *testing.T) {
	now := time.Now().UTC()
	j, _ := NewJunction(testTenantID, testUserGCID, "ca", "cb", []string{testAtomID}, now)
	if err := j.Accept(testAtomIDAlt, now); err == nil {
		t.Error("expected error on atom not in overlap set")
	}
}

func TestJunction_Reject(t *testing.T) {
	now := time.Now().UTC()
	j, _ := NewJunction(testTenantID, testUserGCID, "ca", "cb", []string{testAtomID}, now)
	if err := j.Reject(now.Add(time.Minute)); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if j.Status != JunctionStatusRejected {
		t.Errorf("Status = %q", j.Status)
	}
}

func TestJunction_RejectAfterAcceptFails(t *testing.T) {
	now := time.Now().UTC()
	j, _ := NewJunction(testTenantID, testUserGCID, "ca", "cb", []string{testAtomID}, now)
	_ = j.Accept(testAtomID, now)
	if err := j.Reject(now); err == nil {
		t.Error("expected error: cannot reject accepted junction")
	}
}
