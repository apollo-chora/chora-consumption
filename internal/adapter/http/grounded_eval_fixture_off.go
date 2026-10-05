//go:build !eval_fixture

// grounded_eval_fixture_off.go — the PRODUCTION build of the grounded-search
// eval override: a compiled-out NO-OP. The deterministic test-double
// GroundedSearchPort (grounded_eval_fixture_on.go) exists ONLY in an image built
// with -tags eval_fixture; here it is absent from the binary entirely. This is
// the hard "no stub in prod" guarantee — a prod consumption image cannot wire a
// grounded-search test-double no matter what env is set.
package http

// evalGroundedSearchOverride always returns (nil, nil) in the production build, so
// the router falls through to the real chora-model-gateway grounded-search client.
func evalGroundedSearchOverride(_ func(string) string) (GroundedSearchPort, error) {
	return nil, nil
}
