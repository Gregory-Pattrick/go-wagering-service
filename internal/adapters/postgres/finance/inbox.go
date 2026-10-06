package finance

import (
	"context"
	"errors"
	"time"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/application/financial"
	"github.com/jackc/pgx/v5"
)

func (s *Store) WithinInbox(ctx context.Context, identity financial.InboxIdentity, fn func(financial.Unit) error) error {
	err := s.Within(ctx, func(u *Unit) error {
		// Serialize only this consumer/envelope pair, including when no inbox row
		// exists yet. Hash collisions can cause extra waiting, never a false replay.
		// This transaction-scoped lock is neither a global nor a per-wallet mutex.
		if _, err := u.tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 719322))", identity.Consumer+":"+identity.MessageID); err != nil {
			return err
		}
		var hash string
		err := u.tx.QueryRow(ctx, "SELECT payload_hash FROM wagering.inbox WHERE consumer_name=$1 AND message_id=$2", identity.Consumer, identity.MessageID).Scan(&hash)
		exists := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if exists && hash != identity.Hash {
			return financial.Failure("INBOX_PAYLOAD_CONFLICT")
		}
		var received time.Time
		if err = u.tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&received); err != nil {
			return err
		}
		if err = fn(applicationUnit{u}); err != nil {
			return err
		}
		if exists {
			return nil
		}
		result, err := u.ByKey(ctx, identity.Provider, identity.Key)
		if err != nil {
			return err
		}
		state, err := result.Snapshot()
		if err != nil {
			return err
		}
		var completed time.Time
		if err = u.tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&completed); err != nil {
			return err
		}
		if completed.Before(received) {
			completed = received
		}
		return u.CompleteInbox(ctx, identity.Consumer, identity.MessageID, identity.Hash, state.Input.ID, received, completed)
	})
	return applicationError(err)
}

var _ financial.InboxBackend = (*Store)(nil)
