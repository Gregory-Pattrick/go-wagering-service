package financial

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/ledger"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/money"
	tx "github.com/Gregory-Pattrick/go-wagering-service/internal/domain/transaction"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/wallet"
)

func NewID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}
func CanonicalID(id string) (string, error) {
	id = strings.ToLower(id)
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		return "", Failure("INVALID_IDENTIFIER")
	}
	compact := strings.ReplaceAll(id, "-", "")
	data, err := hex.DecodeString(compact)
	if err != nil || len(data) != 16 {
		return "", Failure("INVALID_IDENTIFIER")
	}
	for _, b := range data {
		if b != 0 {
			return id, nil
		}
	}
	return "", Failure("INVALID_IDENTIFIER")
}
func Text(s string, max int) bool {
	if s == "" || len(s) > max || !utf8.ValidString(s) || strings.TrimSpace(s) != s {
		return false
	}
	for _, r := range s {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}

type WalletResponse struct {
	ID       string      `json:"id"`
	PlayerID string      `json:"playerId"`
	Balance  money.Money `json:"balance"`
	Version  int64       `json:"version"`
}

func WalletView(w wallet.Wallet) (WalletResponse, error) {
	s, err := w.Snapshot()
	if err != nil {
		return WalletResponse{}, err
	}
	return WalletResponse{s.ID, s.PlayerID, s.Balance, s.Version}, nil
}

type TransactionResponse struct {
	TransactionID    string         `json:"transactionId"`
	Status           tx.Status      `json:"status"`
	Balance          *money.Money   `json:"balance,omitempty"`
	WalletVersion    *int64         `json:"walletVersion,omitempty"`
	FailureCode      tx.FailureCode `json:"failureCode,omitempty"`
	IdempotentReplay bool           `json:"idempotentReplay"`
	Retryable        *bool          `json:"retryable,omitempty"`
}

func TransactionView(tr tx.Transaction, replay bool) (TransactionResponse, error) {
	s, err := tr.Snapshot()
	if err != nil {
		return TransactionResponse{}, err
	}
	result := TransactionResponse{TransactionID: s.Input.ID, Status: s.Status, FailureCode: s.FailureCode, IdempotentReplay: replay}
	if s.Result != nil {
		balance, version := s.Result.Balance, s.Result.WalletVersion
		result.Balance = &balance
		result.WalletVersion = &version
	}
	if s.Status == tx.Failed {
		retryable := false
		result.Retryable = &retryable
	}
	return result, nil
}

type LedgerItem struct {
	ID            string           `json:"id"`
	WalletID      string           `json:"walletId"`
	TransactionID string           `json:"transactionId"`
	Direction     ledger.Direction `json:"direction"`
	Money         money.Money      `json:"money"`
	BalanceBefore money.Money      `json:"balanceBefore"`
	BalanceAfter  money.Money      `json:"balanceAfter"`
	WalletVersion int64            `json:"walletVersion"`
	CreatedAt     time.Time        `json:"createdAt"`
}
type LedgerPage struct {
	Items      []LedgerItem `json:"items"`
	NextCursor string       `json:"nextCursor,omitempty"`
}
type Cursor struct{ After, Through int64 }

func (c Cursor) Encode(walletID string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(walletID + ":" + strconv.FormatInt(c.After, 10) + ":" + strconv.FormatInt(c.Through, 10)))
}
func DecodeCursor(encoded, walletID string) (Cursor, error) {
	if encoded == "" {
		return Cursor{}, nil
	}
	if len(encoded) > 160 {
		return Cursor{}, Failure("INVALID_CURSOR")
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return Cursor{}, Failure("INVALID_CURSOR")
	}
	parts := strings.Split(string(data), ":")
	if len(parts) != 3 || parts[0] != walletID {
		return Cursor{}, Failure("INVALID_CURSOR")
	}
	a, e1 := strconv.ParseInt(parts[1], 10, 64)
	b, e2 := strconv.ParseInt(parts[2], 10, 64)
	if e1 != nil || e2 != nil || a < 1 || b < a || strconv.FormatInt(a, 10) != parts[1] || strconv.FormatInt(b, 10) != parts[2] {
		return Cursor{}, Failure("INVALID_CURSOR")
	}
	return Cursor{a, b}, nil
}

type Reconciliation struct {
	WalletID          string      `json:"walletId"`
	StoredBalance     money.Money `json:"storedBalance"`
	CalculatedBalance money.Money `json:"calculatedBalance"`
	Difference        money.Money `json:"difference"`
	Consistent        bool        `json:"consistent"`
	CheckedEntries    int64       `json:"checkedEntries"`
}
