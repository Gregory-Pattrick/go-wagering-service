package financial

import (
	"context"
	"errors"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/auth"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/money"
	tx "github.com/Gregory-Pattrick/go-wagering-service/internal/domain/transaction"
	"strings"
	"testing"
)

func TestCanonicalIdentifiersAndCursor(t *testing.T) {
	id, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := CanonicalID(strings.ToUpper(id))
	if err != nil || normalized != id {
		t.Fatal("UUID normalization failed")
	}
	for _, bad := range []string{"", "00000000-0000-0000-0000-000000000000", " " + id, id + " ", "not-a-uuid"} {
		if _, err := CanonicalID(bad); err == nil {
			t.Fatal("invalid UUID accepted")
		}
	}
	cursor := Cursor{After: 2, Through: 10}
	decoded, err := DecodeCursor(cursor.Encode(id), id)
	if err != nil || decoded != cursor {
		t.Fatal("cursor roundtrip")
	}
	other, _ := NewID()
	if _, err := DecodeCursor(cursor.Encode(id), other); err == nil {
		t.Fatal("cross-wallet cursor accepted")
	}
	for _, bad := range []string{"%%%", strings.Repeat("a", 161), Cursor{After: -1, Through: 10}.Encode(id), Cursor{After: 10, Through: 2}.Encode(id)} {
		if _, err := DecodeCursor(bad, id); err == nil {
			t.Fatal("invalid cursor accepted")
		}
	}
}
func TestAuthorizationPrecedesAnyRepositoryAccess(t *testing.T) {
	service := New(func() (Backend, error) { t.Fatal("repository accessed before authorization"); return nil, nil })
	provider, _ := auth.NewProvider("provider-a", "provider-a")
	internal, _ := auth.NewInternal("wallet-service")
	amount, _ := money.Parse("1.00", money.BRL)
	if _, err := service.Open(context.Background(), provider, "invalid", amount, Metadata{}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal(err)
	}
	if _, err := service.Submit(context.Background(), provider, tx.ExternalInput{ProviderID: "provider-b"}, Metadata{}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal(err)
	}
	if _, err := service.Transaction(context.Background(), internal, "invalid"); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal(err)
	}
	if _, err := service.External(context.Background(), provider, "provider-b", "external"); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal(err)
	}
	if _, err := service.Ledger(context.Background(), provider, "invalid", "", 50); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal(err)
	}
	if _, err := service.Reconcile(context.Background(), provider, "invalid"); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal(err)
	}
}
