package finance

import (
	"context"
	"errors"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/observability/tracing"
	"github.com/jackc/pgx/v5"
)

// Called in the same SQL transaction as the outbox INSERT. Disabled tracing
// produces no carrier and therefore no extra statement or schema dependency.
func (u *Unit) persistOutboxTrace(ctx context.Context, eventID string) error {
	carrier := tracing.Carrier(ctx)
	if carrier["traceparent"] == "" {
		return nil
	}
	_, err := u.tx.Exec(ctx, `INSERT INTO wagering.outbox_trace_context(event_id,traceparent,tracestate) VALUES($1,$2,$3) ON CONFLICT(event_id) DO NOTHING`, eventID, carrier["traceparent"], carrier["tracestate"])
	return err
}
func (s *Store) EventCarrier(ctx context.Context, id string) (map[string]string, error) {
	var parent, state string
	err := s.pool.QueryRow(ctx, "SELECT traceparent,tracestate FROM wagering.outbox_trace_context WHERE event_id=$1", id).Scan(&parent, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return map[string]string{"traceparent": parent, "tracestate": state}, nil
}
func (s *Store) ReferenceCarrier(ctx context.Context, id string) (map[string]string, error) {
	var parent, state string
	err := s.pool.QueryRow(ctx, `SELECT t.traceparent,t.tracestate FROM wagering.outbox o JOIN wagering.outbox_trace_context t USING(event_id) WHERE o.transaction_id=$1 AND o.event_type='WagerTransactionPendingReference'`, id).Scan(&parent, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return map[string]string{"traceparent": parent, "tracestate": state}, nil
}
