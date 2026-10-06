package finance

import (
	"context"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/money"
	tx "github.com/Gregory-Pattrick/go-wagering-service/internal/domain/transaction"
	"github.com/jackc/pgx/v5"
)

const transactionColumns = `id::text,external_id,provider_id,idempotency_key,payload_hash,wallet_id::text,player_id::text,round_id,game_id,kind,amount_minor,currency,reference_external_id,coalesce(resolved_reference_id::text,''),status,failure_code,result_balance_minor,result_version,created_at,updated_at`

func scanTransaction(row pgx.Row) (tx.Transaction, error) {
	var s tx.State
	var amount int64
	var currency, kind, status, code string
	var resultMinor, resultVersion *int64
	i := &s.Input
	if err := row.Scan(&i.ID, &i.ExternalTransactionID, &i.ProviderID, &i.IdempotencyKey, &s.PayloadHash, &i.WalletID, &i.PlayerID, &i.RoundID, &i.GameID, &kind, &amount, &currency, &i.ReferenceExternalTransactionID, &s.ResolvedReferenceID, &status, &code, &resultMinor, &resultVersion, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return tx.Transaction{}, err
	}
	m, err := money.FromMinor(amount, money.Currency(currency))
	if err != nil {
		return tx.Transaction{}, err
	}
	i.Money = m
	i.Kind = tx.Kind(kind)
	s.Status = tx.Status(status)
	s.FailureCode = tx.FailureCode(code)
	if resultMinor != nil && resultVersion != nil {
		balance, err := money.FromMinor(*resultMinor, money.Currency(currency))
		if err != nil {
			return tx.Transaction{}, err
		}
		s.Result = &tx.Result{Balance: balance, WalletVersion: *resultVersion}
	}
	return tx.Rehydrate(s)
}
func (u *Unit) Transaction(ctx context.Context, id string) (tx.Transaction, error) {
	return scanTransaction(u.tx.QueryRow(ctx, "SELECT "+transactionColumns+" FROM wagering.wager_transactions WHERE id=$1 FOR UPDATE", id))
}
func (u *Unit) ByKey(ctx context.Context, provider, key string) (tx.Transaction, error) {
	return scanTransaction(u.tx.QueryRow(ctx, "SELECT "+transactionColumns+" FROM wagering.wager_transactions WHERE provider_id=$1 AND idempotency_key=$2", provider, key))
}
func (u *Unit) ByExternal(ctx context.Context, provider, external string) (tx.Transaction, error) {
	return scanTransaction(u.tx.QueryRow(ctx, "SELECT "+transactionColumns+" FROM wagering.wager_transactions WHERE provider_id=$1 AND external_id=$2", provider, external))
}
func resultColumns(s tx.State) (any, any) {
	if s.Result == nil {
		return nil, nil
	}
	minor, _ := s.Result.Balance.MinorUnits()
	return minor, s.Result.WalletVersion
}

// InsertTransaction does not abort the SQL transaction on an identity conflict,
// allowing callers to resolve both unique identities and compare the stored hash.
func (u *Unit) InsertTransaction(ctx context.Context, tr tx.Transaction) error {
	s, err := tr.Snapshot()
	if err != nil {
		return err
	}
	if err = checkTime(s.CreatedAt, s.UpdatedAt); err != nil {
		return err
	}
	i := s.Input
	amount, _ := i.Money.MinorUnits()
	currency, _ := i.Money.Currency()
	minor, version := resultColumns(s)
	result, err := u.tx.Exec(ctx, `INSERT INTO wagering.wager_transactions(id,external_id,provider_id,idempotency_key,payload_hash,wallet_id,player_id,round_id,game_id,kind,amount_minor,currency,reference_external_id,resolved_reference_id,status,failure_code,result_balance_minor,result_version,created_at,updated_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,NULLIF($14,'')::uuid,$15,$16,$17,$18,$19,$20) ON CONFLICT DO NOTHING`, i.ID, i.ExternalTransactionID, i.ProviderID, i.IdempotencyKey, s.PayloadHash, i.WalletID, i.PlayerID, i.RoundID, i.GameID, string(i.Kind), amount, string(currency), i.ReferenceExternalTransactionID, s.ResolvedReferenceID, string(s.Status), string(s.FailureCode), minor, version, s.CreatedAt, s.UpdatedAt)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}
func (u *Unit) SaveTransaction(ctx context.Context, before, after tx.Transaction) error {
	old, err := before.Snapshot()
	if err != nil {
		return err
	}
	s, err := after.Snapshot()
	if err != nil {
		return err
	}
	if err = checkTime(s.CreatedAt, s.UpdatedAt); err != nil {
		return err
	}
	if old.Input != s.Input || old.PayloadHash != s.PayloadHash {
		return tx.ErrInvalidIdentity
	}
	minor, version := resultColumns(s)
	result, err := u.tx.Exec(ctx, `UPDATE wagering.wager_transactions SET status=$1,failure_code=$2,result_balance_minor=$3,result_version=$4,resolved_reference_id=NULLIF($5,'')::uuid,updated_at=$6 WHERE id=$7 AND status=$8 AND updated_at=$9`, string(s.Status), string(s.FailureCode), minor, version, s.ResolvedReferenceID, s.UpdatedAt, s.Input.ID, string(old.Status), old.UpdatedAt)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}
