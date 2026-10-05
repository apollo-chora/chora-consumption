package weakness_upload

import (
	"errors"
	"testing"
)

func TestSourceMaterialGate_NonSourceMaterialAlwaysAllowed(t *testing.T) {
	// A nil/disabled gate must not affect the existing kinds at all.
	for _, g := range []*SourceMaterialGate{nil, NewSourceMaterialGate(false), NewSourceMaterialGate(true)} {
		for _, k := range []string{KindMarkedTest, KindNotes, KindScribble} {
			if err := g.Check(k, false); err != nil {
				t.Fatalf("gate(%v).Check(%q,false) = %v; want nil", g.Enabled(), k, err)
			}
		}
	}
}

func TestSourceMaterialGate_DisabledRejectsSourceMaterial(t *testing.T) {
	for _, g := range []*SourceMaterialGate{nil, NewSourceMaterialGate(false)} {
		err := g.Check(KindSourceMaterial, true) // even WITH consent, disabled rejects
		if !errors.Is(err, ErrSourceMaterialDisabled) {
			t.Fatalf("disabled gate must reject source_material with ErrSourceMaterialDisabled; got %v", err)
		}
	}
}

func TestSourceMaterialGate_EnabledRequiresConsent(t *testing.T) {
	g := NewSourceMaterialGate(true)
	if err := g.Check(KindSourceMaterial, false); !errors.Is(err, ErrUploadRightsConsentRequired) {
		t.Fatalf("enabled gate without consent must require consent; got %v", err)
	}
	if err := g.Check(KindSourceMaterial, true); err != nil {
		t.Fatalf("enabled gate WITH consent must allow source_material; got %v", err)
	}
}

func TestValidKind_IncludesSourceMaterial(t *testing.T) {
	if !ValidKind(KindSourceMaterial) {
		t.Fatalf("ValidKind(%q) must be true (it is a known kind; acceptance is gated separately)", KindSourceMaterial)
	}
	if IsSourceMaterial(KindMarkedTest) || !IsSourceMaterial(KindSourceMaterial) {
		t.Fatalf("IsSourceMaterial misclassified")
	}
}

func TestNew_CarriesConsentTimestamp(t *testing.T) {
	in := validInput()
	in.UploadKind = KindSourceMaterial
	ts := in.Now
	in.UploadRightsConsentAt = &ts
	u, err := New(in)
	if err != nil {
		t.Fatalf("New(source_material): %v", err)
	}
	if u.UploadRightsConsentAt == nil || !u.UploadRightsConsentAt.Equal(ts) {
		t.Fatalf("consent timestamp not carried: %+v", u.UploadRightsConsentAt)
	}
}
