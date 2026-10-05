// growth_scan_test.go — covers the scan helpers + ListGrowthEvents
// row-iteration path that the basic + coverage tests skip.
package pg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

// stubRows mocks a multi-row Rows iterator over a slice of scan callbacks.
type stubRows struct {
	scans []scanFn
	i     int
	err   error
}

func (s *stubRows) Next() bool {
	return s.err == nil && s.i < len(s.scans)
}
func (s *stubRows) Scan(dest ...any) error {
	if s.i >= len(s.scans) {
		return ErrNoRows
	}
	fn := s.scans[s.i]
	s.i++
	return fn(dest...)
}
func (s *stubRows) Close() error { return nil }
func (s *stubRows) Err() error   { return s.err }

// growthEventScan builds a Scan callback for one companion_growth_events row.
func growthEventScan(eventID string, source string, awardedDelta int, expTotalAfter int, ts time.Time) scanFn {
	return func(dest ...any) error {
		// 17 columns total (mirror selectExistingGrowthEventSQL).
		if len(dest) != 17 {
			return errors.New("dest count mismatch")
		}
		*(dest[0].(*string)) = eventID
		*(dest[1].(*string)) = "tenant-1"
		*(dest[2].(*string)) = "fam-1"
		*(dest[3].(*string)) = "user-1"
		*(dest[4].(*string)) = source
		*(dest[5].(*int)) = awardedDelta
		*(dest[6].(*int)) = awardedDelta
		*(dest[7].(*int)) = expTotalAfter
		*(dest[8].(*bool)) = false
		*(dest[9].(*bool)) = false
		*(dest[10].(*bool)) = false
		*(dest[11].(*string)) = "idem-" + eventID
		// 12-15 — leave as nil *string / *int.
		*(dest[16].(*time.Time)) = ts
		return nil
	}
}

// listQuerier overrides Query to return our row iterator.
type listQuerier struct {
	stubQuerier
	rows *stubRows
}

func (q *listQuerier) Query(_ context.Context, sql string, _ ...any) (Rows, error) {
	q.stubQuerier.execCalls = append(q.stubQuerier.execCalls, "Q:"+sql)
	return q.rows, nil
}

func (q *listQuerier) QueryRow(_ context.Context, sql string, _ ...any) Row {
	q.stubQuerier.execCalls = append(q.stubQuerier.execCalls, "QR:"+sql)
	return nil
}

type listTxRunner struct {
	q *listQuerier
}

func (t *listTxRunner) RunInTx(ctx context.Context, fn func(context.Context, Querier) error) error {
	return fn(ctx, t.q)
}

func TestListGrowthEvents_IteratesRowsAndPaginates(t *testing.T) {
	now := time.Now().UTC()
	rows := &stubRows{
		scans: []scanFn{
			growthEventScan("e1", "atom_session", 3, 3, now),
			growthEventScan("e2", "atom_session", 3, 6, now.Add(1*time.Second)),
			growthEventScan("e3", "hex_expand", 4, 10, now.Add(2*time.Second)),
		},
	}
	tx := &listTxRunner{q: &listQuerier{rows: rows}}
	repo := NewGrowthRepo(tx)

	out, err := repo.ListGrowthEvents(withCtx(), growth.ListGrowthEventsInput{
		TenantID: pgTenantID, CompanionID: "fam-1", PageSize: 2,
	})
	if err != nil {
		t.Fatalf("ListGrowthEvents: %v", err)
	}
	// PageSize+1 fetched → first 2 returned, third becomes the next-token.
	if len(out.Events) != 2 {
		t.Errorf("len(Events) = %d, want 2", len(out.Events))
	}
	if out.NextPageToken == "" {
		t.Errorf("expected NextPageToken when overflow row present")
	}
}

func TestListGrowthEvents_NoPaginationWhenFitsOnePage(t *testing.T) {
	now := time.Now().UTC()
	rows := &stubRows{
		scans: []scanFn{
			growthEventScan("e1", "atom_session", 3, 3, now),
		},
	}
	tx := &listTxRunner{q: &listQuerier{rows: rows}}
	repo := NewGrowthRepo(tx)
	out, err := repo.ListGrowthEvents(withCtx(), growth.ListGrowthEventsInput{
		TenantID: pgTenantID, CompanionID: "fam-1", PageSize: 50,
	})
	if err != nil {
		t.Fatalf("ListGrowthEvents: %v", err)
	}
	if len(out.Events) != 1 {
		t.Errorf("len(Events) = %d, want 1", len(out.Events))
	}
	if out.NextPageToken != "" {
		t.Errorf("expected empty NextPageToken")
	}
}

func TestScanGrowthEventRow_NilRowReturnsNil(t *testing.T) {
	ev, err := scanGrowthEventRow(nil)
	if err != nil {
		t.Errorf("err = %v; want nil", err)
	}
	if ev != nil {
		t.Errorf("ev = %v; want nil", ev)
	}
}

func TestScanGrowthEventRow_ScansAllColumns(t *testing.T) {
	row := stubRow{fn: growthEventScan("e1", "atom_session", 3, 3, time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC))}
	ev, err := scanGrowthEventRow(row)
	if err != nil {
		t.Fatalf("scanGrowthEventRow: %v", err)
	}
	if ev == nil || ev.GrowthEventID != "e1" {
		t.Errorf("ev = %v, want GrowthEventID=e1", ev)
	}
	if ev.Source != "atom_session" {
		t.Errorf("Source = %s", ev.Source)
	}
	if ev.AwardedDelta != 3 {
		t.Errorf("AwardedDelta = %d, want 3", ev.AwardedDelta)
	}
}
