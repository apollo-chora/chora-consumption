// companion_suspension_wiring_test.go: the composition-root guarantees for the
// ADR-252 / ADR-254 D11 advisory containment projection.
//
// The two that matter:
//
//  1. BOTH read surfaces get an advisor, and it is the SAME instance. The chat
//     turn (403 COMPANION_SUSPENDED) and the ADR-235 reflection skip read the
//     same containment state; two projections could answer differently for the
//     same learner in the same second.
//  2. No pg pool still yields a working (in-memory) advisor rather than a nil
//     one. Nil is not a safe default here: it renders no companion_status and
//     silently drops the honest refusal.
package main

import (
	"context"
	"testing"

	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
)

func TestWireCompanionSuspension_WiresTheSameAdvisorOntoBothServers(t *testing.T) {
	srv := httpadapter.NewServer()
	ext := httpadapter.NewExtServer(nil)

	// No pool, no Pub/Sub client: the projector cannot bind, but the advisory
	// READ must still exist so the handlers are not silently ungated.
	wireCompanionSuspension(context.Background(), srv, ext, nil, nil)

	if srv.CompanionSuspension == nil {
		t.Fatal("Server.CompanionSuspension nil after wiring: the chat 403 and companion_status would silently vanish")
	}
	if ext.CompanionSuspension == nil {
		t.Fatal("ExtServer.CompanionSuspension nil after wiring: the ADR-252 Q5 reflection skip would never run")
	}
	if srv.CompanionSuspension != ext.CompanionSuspension {
		t.Error("the chat and the reflection read DIFFERENT projections; they must share one so their answers cannot disagree")
	}
}

func TestNewCompanionSuspensionProjection_NoPool_IsUsableNotNil(t *testing.T) {
	proj := newCompanionSuspensionProjection(nil)
	if proj == nil {
		t.Fatal("newCompanionSuspensionProjection(nil) = nil; want the in-memory fallback")
	}
	st, err := proj.Status(context.Background(), "01957c8c-2222-7000-aaaa-222222222222", "companion_chat_turn_basic")
	if err != nil {
		t.Fatalf("Status on the no-pool fallback: %v", err)
	}
	if st.Paused {
		t.Error("an empty projection reported paused; want not paused")
	}
}
