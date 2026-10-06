package finance

import "context"

// Telemetry is read-only. It never reconstructs or modifies a wallet balance.
func (s *Store) Telemetry(ctx context.Context) (map[string]float64, error) {
	names := []string{"transactions_pending", "transactions_pending_reference", "transactions_processed", "transactions_rejected", "transactions_failed", "outbox_pending", "outbox_oldest_seconds", "outbox_retry_attempts", "references_pending", "references_oldest_seconds", "references_pending_attempts", "inbox_completed"}
	values := make([]float64, len(names))
	dest := make([]any, len(names))
	for i := range values {
		dest[i] = &values[i]
	}
	err := s.pool.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM wagering.wager_transactions WHERE status='PENDING')::float8,
 (SELECT count(*) FROM wagering.wager_transactions WHERE status='PENDING_REFERENCE')::float8,
 (SELECT count(*) FROM wagering.wager_transactions WHERE status='PROCESSED')::float8,
 (SELECT count(*) FROM wagering.wager_transactions WHERE status='REJECTED')::float8,
 (SELECT count(*) FROM wagering.wager_transactions WHERE status='FAILED')::float8,
 (SELECT count(*) FROM wagering.outbox WHERE published_at IS NULL)::float8,
 (SELECT coalesce(greatest(extract(epoch FROM clock_timestamp()-min(occurred_at)),0),0) FROM wagering.outbox WHERE published_at IS NULL)::float8,
 (SELECT coalesce(sum(greatest(attempts-1,0)),0) FROM wagering.outbox)::float8,
 (SELECT count(*) FROM wagering.transaction_work)::float8,
 (SELECT coalesce(greatest(extract(epoch FROM clock_timestamp()-min(t.created_at)),0),0) FROM wagering.transaction_work w JOIN wagering.wager_transactions t ON t.id=w.transaction_id)::float8,
 (SELECT coalesce(sum(attempts),0) FROM wagering.transaction_work)::float8,
 (SELECT count(*) FROM wagering.inbox)::float8`).Scan(dest...)
	if err != nil {
		return nil, err
	}
	result := map[string]float64{}
	for i, name := range names {
		result[name] = values[i]
	}
	return result, nil
}
