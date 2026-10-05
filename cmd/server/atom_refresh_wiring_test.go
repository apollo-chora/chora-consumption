package main

import (
	"testing"

	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
)

// wireAtomRefresh is nil-safe: no ext, or no pool, must be a loud no-op (the
// push handlers then stay dark rather than half-wired).
func TestWireAtomRefresh_NilArgsNoOp(t *testing.T) {
	wireAtomRefresh(nil, nil, nil, nil) // must not panic

	ext := &httpadapter.ExtServer{}
	wireAtomRefresh(ext, nil, nil, nil)
	if ext.AtomRefreshSub != nil {
		t.Fatalf("no pool must leave AtomRefreshSub nil (dark, not half-wired)")
	}
}
