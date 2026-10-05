// mana_client_cover_test.go — supplemental coverage for the gRPC ManaService
// client: the real-dial constructor (target validation + timeout fallback) and
// the stub constructor's timeout fallback.
package clients

import (
	"testing"
	"time"

	identityv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/identity/v1"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// TestProtoManaSourceToCompanion — every proto ManaSource enum value maps to the
// matching companion.ManaSource string; unknown/unspecified maps to the empty
// (unspecified) source.
func TestProtoManaSourceToCompanion(t *testing.T) {
	cases := []struct {
		in   identityv1.ManaSource
		want companion.ManaSource
	}{
		{identityv1.ManaSource_MANA_SOURCE_SUBSCRIPTION_GRANT, companion.ManaSourceSubscriptionGrant},
		{identityv1.ManaSource_MANA_SOURCE_TOPUP, companion.ManaSourceTopup},
		{identityv1.ManaSource_MANA_SOURCE_PROMO, companion.ManaSourcePromo},
		{identityv1.ManaSource_MANA_SOURCE_TENANT_SUBSIDY, companion.ManaSourceTenantSubsidy},
		{identityv1.ManaSource_MANA_SOURCE_REFUND, companion.ManaSourceRefund},
		{identityv1.ManaSource_MANA_SOURCE_ROLLOVER, companion.ManaSourceRollover},
		{identityv1.ManaSource_MANA_SOURCE_MINT, companion.ManaSourceMint},
		{identityv1.ManaSource_MANA_SOURCE_UNSPECIFIED, companion.ManaSourceUnspecified},
	}
	for _, tc := range cases {
		if got := protoManaSourceToCompanion(tc.in); got != tc.want {
			t.Errorf("protoManaSourceToCompanion(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestNewManaClient_DialsAndAppliesTimeout — a valid mesh target constructs a
// client (grpc.NewClient is lazy — no live server needed); a non-positive
// timeout falls back to the 5s default, a positive one is preserved.
func TestNewManaClient_DialsAndAppliesTimeout(t *testing.T) {
	c, err := NewManaClient("chora-identity.identity.svc.cluster.local:9090", 0)
	if err != nil {
		t.Fatalf("NewManaClient err = %v", err)
	}
	if c == nil || c.client == nil {
		t.Fatal("client/conn not constructed")
	}
	if c.timeout != manaDefaultTimeout {
		t.Errorf("timeout = %v, want %v fallback", c.timeout, manaDefaultTimeout)
	}

	c2, err := NewManaClient("chora-identity:9090", 3*time.Second)
	if err != nil {
		t.Fatalf("NewManaClient err = %v", err)
	}
	if c2.timeout != 3*time.Second {
		t.Errorf("timeout = %v, want 3s", c2.timeout)
	}
}

// TestNewManaClientFromStub_TimeoutFallback — the stub constructor applies the
// same non-positive-timeout fallback as the real dial.
func TestNewManaClientFromStub_TimeoutFallback(t *testing.T) {
	c := NewManaClientFromStub(&fakeManaService{}, -1)
	if c.timeout != manaDefaultTimeout {
		t.Errorf("timeout = %v, want %v fallback", c.timeout, manaDefaultTimeout)
	}
}
