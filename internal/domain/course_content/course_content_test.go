package course_content

import (
	"errors"
	"testing"
)

func TestKind_Valid(t *testing.T) {
	for _, k := range []Kind{KindAtom, KindVideo, KindYouTube, KindDocument, KindLiveClassroom, KindAssessment} {
		if !k.Valid() {
			t.Fatalf("%q should be valid", k)
		}
	}
	if Kind("podcast").Valid() {
		t.Fatalf("unknown kind should be invalid")
	}
}

func TestNew_Valid(t *testing.T) {
	it, err := New(NewParams{
		ItemID: "i1", TenantID: "t1", CourseID: "c1",
		Kind: KindAtom, Ref: "019e30db-0000-7000-8000-0000000000a1", Title: "  Intro  ", Position: 0,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if it.Title != "Intro" {
		t.Fatalf("title not trimmed: %q", it.Title)
	}
}

func TestNew_Rejects(t *testing.T) {
	base := NewParams{ItemID: "i1", TenantID: "t1", CourseID: "c1", Kind: KindAtom, Ref: "r", Title: "x", Position: 0}
	mut := func(f func(*NewParams)) NewParams { p := base; f(&p); return p }

	cases := map[string]NewParams{
		"no tenant": mut(func(p *NewParams) { p.TenantID = " " }),
		"no course": mut(func(p *NewParams) { p.CourseID = "" }),
		"no item":   mut(func(p *NewParams) { p.ItemID = "" }),
		"bad kind":  mut(func(p *NewParams) { p.Kind = "bogus" }),
		"no ref":    mut(func(p *NewParams) { p.Ref = "  " }),
		"neg pos":   mut(func(p *NewParams) { p.Position = -1 }),
	}
	for name, p := range cases {
		if _, err := New(p); err == nil {
			t.Fatalf("%s: expected error", name)
		}
	}
	if _, err := New(mut(func(p *NewParams) { p.Kind = "bogus" })); !errors.Is(err, ErrInvalidItem) {
		t.Fatalf("bad kind should be ErrInvalidItem")
	}
}
