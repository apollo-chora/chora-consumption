package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/course_content"
)

func seedAndServe(t *testing.T, tenant, course string, items []*course_content.Item) *httptest.ResponseRecorder {
	t.Helper()
	s := NewExtServer(nil)
	if items != nil {
		if err := s.CourseContent.ReplaceByCourse(context.Background(), tenant, course, items); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/me/courses/"+course+"/content", nil)
	req.Header.Set("X-Tenant-Id", tenant)
	req.Header.Set("gcid", "00000000-0000-7000-8000-000000001999")
	rec := httptest.NewRecorder()
	s.handleMeCourseContent(rec, req)
	return rec
}

func mkItem(t *testing.T, tenant, course, id string, kind course_content.Kind, ref string, pos int) *course_content.Item {
	t.Helper()
	it, err := course_content.New(course_content.NewParams{
		ItemID: id, TenantID: tenant, CourseID: course, Kind: kind, Ref: ref, Title: id, Position: pos,
	})
	if err != nil {
		t.Fatalf("mkItem: %v", err)
	}
	return it
}

func TestReadCourseContent_ReturnsOrderedItems(t *testing.T) {
	const tn, cr = "11111111-1111-7111-8111-111111111111", "019e30db-692f-7d10-8ce0-59669fe9298d"
	items := []*course_content.Item{
		mkItem(t, tn, cr, "i1", course_content.KindAtom, "019e30db-0000-7000-8000-0000000000a1", 0),
		mkItem(t, tn, cr, "i2", course_content.KindYouTube, "https://youtube.com/watch?v=x", 1),
	}
	rec := seedAndServe(t, tn, cr, items)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		CourseID string `json:"course_id"`
		Items    []struct {
			Kind string `json:"kind"`
		} `json:"items"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Items) != 2 || resp.Items[0].Kind != "atom" || resp.Items[1].Kind != "youtube" {
		t.Fatalf("unexpected items: %s", rec.Body.String())
	}
}

func TestReadCourseContent_EmptyCourse_Returns200Empty(t *testing.T) {
	rec := seedAndServe(t, "11111111-1111-7111-8111-111111111111", "019e30db-692f-7d10-8ce0-59669fe9298d", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var resp struct {
		Items []any `json:"items"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Items) != 0 {
		t.Fatalf("want 0 items, got %d", len(resp.Items))
	}
}

func TestReadCourseContent_MissingTenant_400(t *testing.T) {
	s := NewExtServer(nil)
	req := httptest.NewRequest(http.MethodGet, "/v1/me/courses/c1/content", nil)
	rec := httptest.NewRecorder()
	s.handleMeCourseContent(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing tenant: want 400, got %d", rec.Code)
	}
}

func TestReadCourseContent_BadPath_404(t *testing.T) {
	s := NewExtServer(nil)
	req := httptest.NewRequest(http.MethodGet, "/v1/me/courses/c1", nil)
	req.Header.Set("X-Tenant-Id", "t1")
	req.Header.Set("gcid", "g1")
	rec := httptest.NewRecorder()
	s.handleMeCourseContent(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("bad path: want 404, got %d", rec.Code)
	}
}
