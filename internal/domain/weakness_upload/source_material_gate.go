// source_material_gate.go — WS-5 (ADR-205 D8 / CHO-1957) acceptance gate for the
// 'source_material' upload kind: a grounding textbook used as TRANSIENT context
// for the analyse pass. It is the highest IP-risk input (third-party copyright),
// so before it is accepted the learner must give a one-tap upload-rights
// attestation, and it must be shredded on the same envelope path as the rest
// (never persisted verbatim, never reproduced into generated atoms — the
// orchestrator distils it to a scope-profile, that step is cross-service).
//
// DARK by default: the gate is constructed disabled (WEAKNESS_SOURCE_MATERIAL_
// ENABLED off) so the live path keeps rejecting source_material until the owner
// cuts the graduated crew (which knows how to treat it as grounding) over and
// the crypto-shred path is wired. The other upload kinds are never affected.
package weakness_upload

import "errors"

var (
	// ErrSourceMaterialDisabled — a source_material upload arrived but the kind
	// is not enabled (DARK). The handler maps this to 403/400. The other kinds
	// are never gated by this.
	ErrSourceMaterialDisabled = errors.New("weakness_upload: source_material uploads are not enabled")

	// ErrUploadRightsConsentRequired — a source_material upload arrived without
	// the one-tap upload-rights attestation. The handler maps this to 403. We
	// never accept a textbook (third-party IP) without the learner attesting
	// they have the right to upload it.
	ErrUploadRightsConsentRequired = errors.New("weakness_upload: source_material requires a one-tap upload-rights consent")
)

// IsSourceMaterial reports whether the kind is the transient grounding textbook.
func IsSourceMaterial(kind string) bool { return kind == KindSourceMaterial }

// SourceMaterialGate decides whether a source_material upload is accepted. nil or
// disabled rejects source_material outright (DARK); enabled requires the one-tap
// upload-rights consent. Non-source_material kinds always pass (this gate is
// orthogonal to them).
type SourceMaterialGate struct {
	enabled bool
}

// NewSourceMaterialGate constructs the gate.
func NewSourceMaterialGate(enabled bool) *SourceMaterialGate {
	return &SourceMaterialGate{enabled: enabled}
}

// Enabled reports whether source_material uploads are accepted.
func (g *SourceMaterialGate) Enabled() bool { return g != nil && g.enabled }

// Check enforces the source_material policy for one upload:
//   - non-source_material kind → nil (untouched),
//   - source_material + disabled/nil gate → ErrSourceMaterialDisabled,
//   - source_material + enabled + no consent → ErrUploadRightsConsentRequired,
//   - source_material + enabled + consent → nil.
func (g *SourceMaterialGate) Check(kind string, consentGiven bool) error {
	if !IsSourceMaterial(kind) {
		return nil
	}
	if g == nil || !g.enabled {
		return ErrSourceMaterialDisabled
	}
	if !consentGiven {
		return ErrUploadRightsConsentRequired
	}
	return nil
}
