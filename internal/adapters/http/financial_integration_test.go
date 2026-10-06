//go:build apiintegration

package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/adapters/oidcauth"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/adapters/postgres/finance"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/adapters/postgres/migrations"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/application/financial"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/config"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"
)

type apiView struct {
	ID            string `json:"id"`
	TransactionID string `json:"transactionId"`
	Status        string `json:"status"`
	Balance       struct {
		Amount   string `json:"amount"`
		Currency string `json:"currency"`
	} `json:"balance"`
	Version          int64  `json:"version"`
	WalletVersion    int64  `json:"walletVersion"`
	FailureCode      string `json:"failureCode"`
	IdempotentReplay bool   `json:"idempotentReplay"`
	NextCursor       string `json:"nextCursor"`
	Items            []struct {
		ID string `json:"id"`
	} `json:"items"`
	Consistent     bool  `json:"consistent"`
	CheckedEntries int64 `json:"checkedEntries"`
	Difference     struct {
		Amount string `json:"amount"`
	} `json:"difference"`
}

func TestFinancialHTTPWithPostgresAndKeycloak(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	migrationURL, appURL := os.Getenv("FINANCE_TEST_MIGRATION_URL"), os.Getenv("FINANCE_TEST_DATABASE_URL")
	for _, raw := range []string{migrationURL, appURL} {
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() != "finance-postgres" || u.Path != "/wagering" {
			t.Fatal("refusing database reset outside isolated finance-postgres/wagering")
		}
	}
	if err := migrations.Run(ctx, migrationURL, 0); err != nil {
		t.Fatal(err)
	}
	if err := migrations.Run(ctx, migrationURL, migrations.Latest()); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	cfg, err := config.LoadAuth()
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	var authentication *Authentication
	app := fx.New(fx.Supply(cfg, logger), fx.Provide(oidcauth.NewVerifier, NewAuthentication), fx.Populate(&authentication), fx.NopLogger)
	if err = app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if err := app.Stop(cleanup); err != nil {
			t.Error(err)
		}
	}()
	makeServer := func(db *pgxpool.Pool) *httptest.Server {
		service := financial.New(func() (financial.Backend, error) { return finance.New(db), nil })
		mux := NewRouter(logger)
		RegisterFinancialRoutes(mux, NewFinancialAPI(service, logger))
		return httptest.NewServer(authentication.Protect(mux))
	}
	server := makeServer(pool)
	defer server.Close()
	client := &http.Client{Timeout: 15 * time.Second}
	defer client.CloseIdleConnections()
	tokens := map[string]string{}
	for _, actor := range []string{"wallet-service", "provider-a", "provider-b"} {
		body := url.Values{"grant_type": {"client_credentials"}, "client_id": {actor}, "client_secret": {actor + "-local-secret"}}
		request, err := http.NewRequestWithContext(ctx, "POST", os.Getenv("KEYCLOAK_TOKEN_URL"), strings.NewReader(body.Encode()))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		var token struct {
			AccessToken string `json:"access_token"`
		}
		err = json.NewDecoder(response.Body).Decode(&token)
		response.Body.Close()
		if err != nil || response.StatusCode != 200 || token.AccessToken == "" {
			t.Fatalf("could not obtain %s token", actor)
		}
		tokens[actor] = token.AccessToken
	}
	send := func(base, method, path, actor, key, body string) (int, []byte, error) {
		request, err := http.NewRequestWithContext(ctx, method, base+path, strings.NewReader(body))
		if err != nil {
			return 0, nil, err
		}
		request.Header.Set("Content-Type", "application/json")
		if actor != "" {
			request.Header.Set("Authorization", "Bearer "+tokens[actor])
		}
		if key != "" {
			request.Header.Set("Idempotency-Key", key)
		}
		response, err := client.Do(request)
		if err != nil {
			return 0, nil, err
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		return response.StatusCode, data, err
	}
	call := func(method, path, actor, key, body string, want int) apiView {
		t.Helper()
		status, data, err := send(server.URL, method, path, actor, key, body)
		if err != nil {
			t.Fatal(err)
		}
		if status != want {
			t.Fatalf("%s %s: got %d, want %d: %s", method, path, status, want, data)
		}
		var result apiView
		if err = json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	identifier := func() string {
		t.Helper()
		id, err := financial.NewID()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	open := func(amount string) (string, string) {
		t.Helper()
		player := identifier()
		body := fmt.Sprintf(`{"playerId":%q,"initialBalance":{"amount":%q,"currency":"BRL"}}`, player, amount)
		result := call("POST", "/wallets", "wallet-service", "", body, 201)
		if result.Balance.Amount != amount || result.Version != 1 {
			t.Fatal("invalid opening")
		}
		return result.ID, player
	}
	input := func(wallet, player, external, kind, amount, reference string) string {
		fields := map[string]any{"providerId": "provider-a", "externalTransactionId": external, "walletId": wallet, "playerId": player, "roundId": "round", "gameId": "game", "kind": kind, "money": map[string]string{"amount": amount, "currency": "BRL"}}
		if reference != "" {
			fields["referenceExternalTransactionId"] = reference
		}
		data, _ := json.Marshal(fields)
		return string(data)
	}
	t.Run("opening authentication and uniqueness", func(t *testing.T) {
		body := fmt.Sprintf(`{"playerId":%q,"initialBalance":{"amount":"100.00","currency":"BRL"}}`, identifier())
		call("POST", "/wallets", "", "", body, 401)
		call("POST", "/wallets", "provider-a", "", body, 403)
		first := call("POST", "/wallets", "wallet-service", "", body, 201)
		call("POST", "/wallets", "wallet-service", "", body, 409)
		call("GET", "/wallets/"+first.ID, "provider-a", "", "", 403)
		call("GET", "/wallets/"+first.ID, "wallet-service", "", "", 200)
		id, _ := open("0.00")
		page := call("GET", "/wallets/"+id+"/ledger", "wallet-service", "", "", 200)
		if len(page.Items) != 0 {
			t.Fatal("zero opening has ledger")
		}
	})
	t.Run("replay conflicts original result and provider isolation", func(t *testing.T) {
		wallet, player := open("100.00")
		external, key := identifier(), identifier()
		body := input(wallet, player, external, "BET", "80.00", "")
		first := call("POST", "/wagering/transactions", "provider-a", key, body, 200)
		if first.Balance.Amount != "20.00" || first.IdempotentReplay {
			t.Fatal("incorrect first decision")
		}
		call("POST", "/wagering/transactions", "provider-a", identifier(), input(wallet, player, identifier(), "WIN", "30.00", ""), 200)
		replay := call("POST", "/wagering/transactions", "provider-a", key, body, 200)
		if replay.TransactionID != first.TransactionID || replay.Balance.Amount != "20.00" || !replay.IdempotentReplay {
			t.Fatal("incorrect replay")
		}
		normalizedBody := strings.ReplaceAll(strings.ReplaceAll(body, wallet, strings.ToUpper(wallet)), player, strings.ToUpper(player))
		normalized := call("POST", "/wagering/transactions", "provider-a", key, normalizedBody, 200)
		if !normalized.IdempotentReplay || normalized.TransactionID != first.TransactionID {
			t.Fatal("UUID case changed business identity")
		}
		call("POST", "/wagering/transactions", "provider-a", key, input(wallet, player, external, "BET", "79.00", ""), 409)
		call("POST", "/wagering/transactions", "provider-a", identifier(), body, 409)
		call("POST", "/wagering/transactions", "provider-b", key, body, 403)
		call("POST", "/wagering/transactions", "wallet-service", key, body, 403)
		call("GET", "/wagering/transactions/"+first.TransactionID, "provider-b", "", "", 404)
		call("GET", "/providers/provider-a/wagering/transactions/"+external, "provider-b", "", "", 403)
		call("GET", "/providers/provider-b/wagering/transactions/"+external, "provider-b", "", "", 404)
		got := call("GET", "/wagering/transactions/"+first.TransactionID, "provider-a", "", "", 200)
		if got.Balance.Amount != "20.00" {
			t.Fatal("query returned current wallet balance")
		}
		call("GET", "/providers/provider-a/wagering/transactions/"+external, "provider-a", "", "", 200)
		// A separate pool and handler have no process-local replay state.
		secondPool, err := pgxpool.New(ctx, appURL)
		if err != nil {
			t.Fatal(err)
		}
		defer secondPool.Close()
		second := makeServer(secondPool)
		defer second.Close()
		status, data, err := send(second.URL, "POST", "/wagering/transactions", "provider-a", key, body)
		if err != nil || status != 200 {
			t.Fatalf("fresh handler replay: %d %v %s", status, err, data)
		}
		var persisted apiView
		if err = json.Unmarshal(data, &persisted); err != nil {
			t.Fatal(err)
		}
		if !persisted.IdempotentReplay || persisted.TransactionID != first.TransactionID {
			t.Fatal("replay was not persistent")
		}
		page := call("GET", "/wallets/"+wallet+"/ledger?limit=1", "wallet-service", "", "", 200)
		seen := map[string]bool{}
		for {
			for _, item := range page.Items {
				if seen[item.ID] {
					t.Fatal("duplicate pagination item")
				}
				seen[item.ID] = true
			}
			if page.NextCursor == "" {
				break
			}
			page = call("GET", "/wallets/"+wallet+"/ledger?limit=1&cursor="+url.QueryEscape(page.NextCursor), "wallet-service", "", "", 200)
		}
		if len(seen) != 3 {
			t.Fatal("wrong ledger count")
		}
		check := call("POST", "/wallets/"+wallet+"/reconciliation", "wallet-service", "", "", 200)
		if !check.Consistent || check.CheckedEntries != 3 || check.Difference.Amount != "0.00" {
			t.Fatal("reconciliation failed")
		}
	})
	t.Run("business outcomes reference wait and validation", func(t *testing.T) {
		wallet, player := open("10.00")
		rejected := call("POST", "/wagering/transactions", "provider-a", identifier(), input(wallet, player, identifier(), "BET", "11.00", ""), 422)
		if rejected.FailureCode != "INSUFFICIENT_FUNDS" || rejected.Balance.Amount != "10.00" {
			t.Fatal("incorrect rejection")
		}
		missing := call("POST", "/wagering/transactions", "provider-a", identifier(), input(identifier(), player, identifier(), "BET", "1.00", ""), 422)
		if missing.FailureCode != "WALLET_NOT_FOUND" || missing.Balance.Amount != "" {
			t.Fatal("fabricated missing-wallet balance")
		}
		pendingBody := input(wallet, player, identifier(), "REFUND", "1.00", "not-arrived")
		key := identifier()
		waiting := call("POST", "/wagering/transactions", "provider-a", key, pendingBody, 202)
		again := call("POST", "/wagering/transactions", "provider-a", key, pendingBody, 202)
		if waiting.Status != "PENDING_REFERENCE" || !again.IdempotentReplay {
			t.Fatal("pending replay failed")
		}
		loss := call("POST", "/wagering/transactions", "provider-a", identifier(), input(wallet, player, identifier(), "LOSS", "0.00", ""), 200)
		if loss.WalletVersion != 1 {
			t.Fatal("LOSS changed version")
		}
		call("POST", "/wagering/transactions", "provider-a", "", input(wallet, player, identifier(), "BET", "1.00", ""), 400)
		call("POST", "/wagering/transactions", "provider-a", identifier(), input(wallet, player, identifier(), "OPENING", "1.00", ""), 400)
		call("POST", "/wagering/transactions", "provider-a", identifier(), input(wallet, player, identifier(), "WIN", "0.00", ""), 400)
		call("GET", "/wallets/"+wallet+"/ledger?limit=0", "wallet-service", "", "", 400)
		call("GET", "/wallets/"+wallet+"/ledger?cursor=invalid", "wallet-service", "", "", 400)
		var count int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM wagering.outbox WHERE transaction_id=$1", waiting.TransactionID).Scan(&count); err != nil || count != 1 {
			t.Fatal("pending replay duplicated events", err, count)
		}
	})
	t.Run("concurrent duplicate HTTP requests", func(t *testing.T) {
		wallet, player := open("100.00")
		key := identifier()
		body := input(wallet, player, identifier(), "BET", "80.00", "")
		type answer struct {
			status int
			data   []byte
			err    error
		}
		results := make(chan answer, 12)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := 0; i < 12; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				status, data, err := send(server.URL, "POST", "/wagering/transactions", "provider-a", key, body)
				results <- answer{status, data, err}
			}()
		}
		close(start)
		wg.Wait()
		close(results)
		fresh := 0
		transactionID := ""
		for result := range results {
			if result.err != nil || result.status != 200 {
				t.Fatalf("duplicate request: %d %v %s", result.status, result.err, result.data)
			}
			var view apiView
			if err := json.Unmarshal(result.data, &view); err != nil {
				t.Fatal(err)
			}
			if transactionID == "" {
				transactionID = view.TransactionID
			}
			if transactionID != view.TransactionID || view.Balance.Amount != "20.00" {
				t.Fatal("duplicate result mismatch")
			}
			if !view.IdempotentReplay {
				fresh++
			}
		}
		if fresh != 1 {
			t.Fatal("more than one original decision")
		}
		final := call("GET", "/wallets/"+wallet, "wallet-service", "", "", 200)
		if final.Balance.Amount != "20.00" || final.Version != 2 {
			t.Fatal("duplicate financial effect")
		}
	})
}
