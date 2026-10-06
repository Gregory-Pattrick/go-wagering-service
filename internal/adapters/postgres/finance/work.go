package finance

import (
	"context"
	"errors"
	"time"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/application/delivery"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/application/financial"
	tx "github.com/Gregory-Pattrick/go-wagering-service/internal/domain/transaction"
	"github.com/jackc/pgx/v5"
)

type Work = delivery.ReferenceWork

// Every claim uses a fresh owner token, including repeated claims by one process.
// Database time is the authority for scheduling and lease expiration.
func (s *Store) ClaimReference(ctx context.Context, owner string, lease time.Duration) (Work, error) {
	var w Work
	w.Owner = owner
	err := s.pool.QueryRow(ctx, `WITH candidate AS (
 SELECT transaction_id FROM wagering.transaction_work
 WHERE next_attempt_at<=clock_timestamp() AND (lease_until IS NULL OR lease_until<=clock_timestamp())
 ORDER BY next_attempt_at,transaction_id FOR UPDATE SKIP LOCKED LIMIT 1
 ) UPDATE wagering.transaction_work w SET lease_owner=$1,
 lease_until=clock_timestamp()+($2::bigint*interval '1 microsecond'),attempts=w.attempts+1
 FROM candidate c WHERE w.transaction_id=c.transaction_id
 RETURNING w.transaction_id::text,w.attempts`, owner, lease.Microseconds()).Scan(&w.ID, &w.Attempts)
	return w, err
}

// ResumeReference fences the claim again in the financial SQL transaction.
// No network calls happen while locks are held. A terminal decision atomically
// removes work and writes the ledger, journal and immutable domain events.
func (s *Store) ResumeReference(ctx context.Context, w Work, delay, ttl time.Duration) (string, error) {
	outcome := ""
	err := s.Within(ctx, func(u *Unit) error {
		var owner string
		var leaseUntil *time.Time
		var expires, now time.Time
		err := u.tx.QueryRow(ctx, `SELECT coalesce(lease_owner,''),lease_until,expires_at,clock_timestamp()
   FROM wagering.transaction_work WHERE transaction_id=$1 FOR UPDATE`, w.ID).Scan(&owner, &leaseUntil, &expires, &now)
		if errors.Is(err, pgx.ErrNoRows) {
			return delivery.ErrLeaseLost
		}
		if err != nil {
			return err
		}
		if owner != w.Owner || leaseUntil == nil || !leaseUntil.After(now) {
			return delivery.ErrLeaseLost
		}
		before, err := u.Transaction(ctx, w.ID)
		if err != nil {
			return err
		}
		state, err := before.Snapshot()
		if err != nil {
			return err
		}
		if state.Status != tx.Pending && state.Status != tx.PendingReference {
			return delivery.ErrLeaseLost
		}
		wallet, err := u.LockWallet(ctx, state.Input.WalletID)
		if err != nil {
			return err
		}
		// Recheck time after waiting for the wallet lock.
		if err = u.tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
			return err
		}
		if !leaseUntil.After(now) {
			return delivery.ErrLeaseLost
		}
		now = now.UTC().Truncate(time.Microsecond)
		deadline := state.CreatedAt.Add(ttl)
		if expires.Before(deadline) {
			deadline = expires
		}
		var correlation string
		err = u.tx.QueryRow(ctx, `SELECT payload->>'correlationId' FROM wagering.outbox
   WHERE transaction_id=$1 AND event_type='WagerTransactionPendingReference'`, w.ID).Scan(&correlation)
		if errors.Is(err, pgx.ErrNoRows) {
			correlation = w.ID
		} else if err != nil {
			return err
		}
		d, metadata, err := financial.ResumeDecision(ctx, applicationUnit{u}, before, wallet, now, !now.Before(deadline), correlation)
		if err != nil {
			return err
		}
		result, err := d.Transaction.Snapshot()
		if err != nil {
			return err
		}
		outcome = string(result.Status)
		if result.Status == tx.Pending || result.Status == tx.PendingReference {
			// An unchanged pending state emits no duplicate logical event. A stored
			// PENDING row can still transition to PENDING_REFERENCE through this path.
			next := now.Add(delay)
			if next.After(deadline) {
				next = deadline
			}
			if err = u.SaveDecision(ctx, before, d, metadata, &Schedule{NextAttemptAt: next, ExpiresAt: deadline}); err != nil {
				return err
			}
			_, err = u.tx.Exec(ctx, `UPDATE wagering.transaction_work SET next_attempt_at=$2,
    lease_owner=NULL,lease_until=NULL WHERE transaction_id=$1`, w.ID, next)
			return err
		}
		return u.SaveDecision(ctx, before, d, metadata, nil)
	})
	return outcome, err
}
func (s *Store) RetryReference(ctx context.Context, w Work, delay time.Duration) error {
	result, err := s.pool.Exec(ctx, `UPDATE wagering.transaction_work SET
 next_attempt_at=LEAST(expires_at,clock_timestamp()+($3::bigint*interval '1 microsecond')),
 lease_owner=NULL,lease_until=NULL WHERE transaction_id=$1 AND lease_owner=$2 AND lease_until>clock_timestamp()`, w.ID, w.Owner, delay.Microseconds())
	if err == nil && result.RowsAffected() != 1 {
		return delivery.ErrLeaseLost
	}
	return err
}

func (s *Store) ClaimEvent(ctx context.Context, owner string, lease time.Duration) (delivery.Event, error) {
	event := delivery.Event{Owner: owner}
	err := s.pool.QueryRow(ctx, `WITH candidate AS (
 SELECT o.event_id FROM wagering.outbox o
 WHERE o.published_at IS NULL AND o.next_attempt_at<=clock_timestamp()
 AND (o.lease_until IS NULL OR o.lease_until<=clock_timestamp())
 AND NOT EXISTS (SELECT 1 FROM wagering.outbox older WHERE older.aggregate_id=o.aggregate_id
  AND older.published_at IS NULL AND (older.occurred_at,older.event_id)<(o.occurred_at,o.event_id))
 ORDER BY o.occurred_at,o.event_id FOR UPDATE OF o SKIP LOCKED LIMIT 1
 ) UPDATE wagering.outbox o SET lease_owner=$1,
 lease_until=clock_timestamp()+($2::bigint*interval '1 microsecond'),attempts=o.attempts+1
 FROM candidate c WHERE o.event_id=c.event_id
 RETURNING o.event_id::text,o.aggregate_id::text,o.payload::text,o.attempts`, owner, lease.Microseconds()).Scan(&event.ID, &event.AggregateID, &event.Payload, &event.Attempts)
	return event, err
}
func (s *Store) ConfirmEvent(ctx context.Context, event delivery.Event) error {
	result, err := s.pool.Exec(ctx, `UPDATE wagering.outbox SET published_at=clock_timestamp(),lease_owner=NULL,lease_until=NULL
 WHERE event_id=$1 AND lease_owner=$2 AND lease_until>clock_timestamp() AND published_at IS NULL`, event.ID, event.Owner)
	if err == nil && result.RowsAffected() != 1 {
		return delivery.ErrLeaseLost
	}
	return err
}
func (s *Store) RetryEvent(ctx context.Context, event delivery.Event, delay time.Duration) error {
	result, err := s.pool.Exec(ctx, `UPDATE wagering.outbox SET next_attempt_at=clock_timestamp()+($3::bigint*interval '1 microsecond'),lease_owner=NULL,lease_until=NULL
 WHERE event_id=$1 AND lease_owner=$2 AND lease_until>clock_timestamp() AND published_at IS NULL`, event.ID, event.Owner, delay.Microseconds())
	if err == nil && result.RowsAffected() != 1 {
		return delivery.ErrLeaseLost
	}
	return err
}

var _ delivery.Outbox = (*Store)(nil)
