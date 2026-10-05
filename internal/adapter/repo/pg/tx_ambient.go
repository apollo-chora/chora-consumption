// tx_ambient.go — ambient-transaction propagation for same-DB atomic
// composition (CHO-2039 / CR §8 R6-3).
//
// GrowthRepo.AwardExpTx invokes the domain-supplied PostAwardInTx hook with
// a ctx carrying the transaction's open Querier. Any repo in this package
// called from inside that hook (e.g. CompanionLoadoutRepo species-Path grant
// mints — every table involved lives in the same chora_consumption
// database) has its RunInTx JOIN that transaction instead of beginning its
// own, so the EXP award and the Path grants commit or roll back together.
//
// Scope guard: the ONLY producer of an ambient Querier is the AwardExpTx
// hook seam; top-level repo calls never see one and keep their own
// begin/commit lifecycle. Outbox writes are unaffected — they ride the
// separate outbox SQLDB connection and stay post-commit because the domain
// service publishes only after AwardExpTx returns.
package pg

import "context"

// ambientQuerierKey is the private ctx key carrying an open transaction's
// Querier.
type ambientQuerierKey struct{}

// WithAmbientQuerier returns a ctx that carries an open transaction's
// Querier. TxRunner implementations join it (see PgxTxRunner.RunInTx); the
// transaction OWNER — the RunInTx call that began it — commits or rolls
// back.
func WithAmbientQuerier(ctx context.Context, q Querier) context.Context {
	return context.WithValue(ctx, ambientQuerierKey{}, q)
}

// AmbientQuerier reports the open-transaction Querier carried by ctx, if
// any.
func AmbientQuerier(ctx context.Context) (Querier, bool) {
	q, ok := ctx.Value(ambientQuerierKey{}).(Querier)
	if !ok || q == nil {
		return nil, false
	}
	return q, true
}
