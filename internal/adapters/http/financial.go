package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/application/financial"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/auth"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/money"
	tx "github.com/Gregory-Pattrick/go-wagering-service/internal/domain/transaction"
)

type FinancialAPI struct {
	service *financial.Service
	logger  *slog.Logger
}

func NewFinancialAPI(service *financial.Service, logger *slog.Logger) *FinancialAPI {
	return &FinancialAPI{service, logger}
}
func RegisterFinancialRoutes(mux *http.ServeMux, api *FinancialAPI) {
	mux.Handle("POST /wallets", RequireInternal(api.wrap(api.open)))
	mux.Handle("GET /wallets/{walletId}", RequireInternal(api.wrap(api.wallet)))
	mux.Handle("GET /wallets/{walletId}/ledger", RequireInternal(api.wrap(api.ledger)))
	mux.Handle("POST /wallets/{walletId}/reconciliation", RequireInternal(api.wrap(api.reconcile)))
	mux.Handle("POST /wagering/transactions", RequireProvider(api.wrap(api.submit)))
	mux.Handle("GET /wagering/transactions/{transactionId}", RequireProvider(api.wrap(api.transaction)))
	mux.Handle("GET /providers/{providerId}/wagering/transactions/{externalTransactionId}", RequireProvider(api.wrap(api.external)))
	mux.Handle("GET /metrics", RequireInternal(http.HandlerFunc(api.metrics)))
}

type requestMetadataKey struct{}

func metadata(r *http.Request) financial.Metadata {
	m, _ := r.Context().Value(requestMetadataKey{}).(financial.Metadata)
	return m
}
func (a *FinancialAPI) wrap(handler func(http.ResponseWriter, *http.Request) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		correlation, err := financial.NewID()
		if err != nil {
			w.WriteHeader(503)
			return
		}
		values := r.Header.Values("X-Correlation-ID")
		if len(values) > 1 || (len(values) == 1 && !financial.Text(values[0], 128)) {
			a.respondError(w, financial.Failure("INVALID_METADATA"), correlation)
			return
		}
		if len(values) == 1 {
			correlation = values[0]
		}
		w.Header().Set("X-Correlation-ID", correlation)
		w.Header().Set("Cache-Control", "no-store")
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		r = r.WithContext(context.WithValue(ctx, requestMetadataKey{}, financial.Metadata{CorrelationID: correlation}))
		if err = handler(w, r); err != nil {
			a.respondError(w, err, correlation)
		}
	})
}
func respond(w http.ResponseWriter, status int, body any) error {
	// Encode before writing headers so an invalid domain value cannot cause a
	// partial success response. Once headers are written a transport write failure
	// never becomes a second response or another financial operation.
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(data, '\n'))
	return nil
}
func (a *FinancialAPI) respondError(w http.ResponseWriter, err error, correlation string) {
	status, code, retryable := 503, "TEMPORARY_UNAVAILABLE", true
	var validation *financial.Error
	switch {
	case errors.Is(err, auth.ErrForbidden):
		status, code, retryable = 403, "FORBIDDEN", false
	case errors.Is(err, financial.ErrNotFound):
		status, code, retryable = 404, "NOT_FOUND", false
	case errors.As(err, &validation):
		status, code, retryable = 400, validation.Code, false
		if code == "WALLET_ALREADY_EXISTS" || code == "IDEMPOTENCY_CONFLICT" || code == "EXTERNAL_TRANSACTION_CONFLICT" {
			status = 409
		}
		if code == "PAYLOAD_TOO_LARGE" {
			status = 413
		}
		if code == "UNSUPPORTED_MEDIA_TYPE" {
			status = 415
		}
	case errors.Is(err, financial.ErrOutcomeUnknown):
		code = "COMMIT_OUTCOME_UNKNOWN"
	}
	if status == 503 {
		w.Header().Set("Retry-After", "1")
		a.logger.Error("financial request unavailable", "correlationId", correlation, "code", code)
	}
	_ = respond(w, status, map[string]any{"error": map[string]any{"code": code, "message": message(code), "correlationId": correlation, "retryable": retryable}})
}
func message(code string) string {
	switch code {
	case "FORBIDDEN":
		return "Access is not permitted"
	case "NOT_FOUND":
		return "Resource not found"
	case "TEMPORARY_UNAVAILABLE", "COMMIT_OUTCOME_UNKNOWN":
		return "Retry with the same operation identity and idempotency key"
	case "IDEMPOTENCY_CONFLICT", "EXTERNAL_TRANSACTION_CONFLICT":
		return "The operation identity conflicts with an existing request"
	case "WALLET_ALREADY_EXISTS":
		return "A wallet already exists for this player and currency"
	default:
		return "Request validation failed"
	}
}

// readObject rejects unknown, duplicate and incorrectly cased top-level keys.
// Monetary subobjects are validated independently by money.ParseJSON.
func readObject(w http.ResponseWriter, r *http.Request, allowed ...string) (map[string]json.RawMessage, error) {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return nil, financial.Failure("UNSUPPORTED_MEDIA_TYPE")
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		var limit *http.MaxBytesError
		if errors.As(err, &limit) {
			return nil, financial.Failure("PAYLOAD_TOO_LARGE")
		}
		return nil, financial.Failure("INVALID_JSON")
	}
	if !utf8.Valid(data) {
		return nil, financial.Failure("INVALID_JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, financial.Failure("INVALID_JSON")
	}
	keys := make(map[string]bool)
	for _, key := range allowed {
		keys[key] = true
	}
	result := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return nil, financial.Failure("INVALID_JSON")
		}
		key, ok := token.(string)
		if !ok || !keys[key] {
			return nil, financial.Failure("INVALID_JSON")
		}
		if _, exists := result[key]; exists {
			return nil, financial.Failure("INVALID_JSON")
		}
		var value json.RawMessage
		if err = decoder.Decode(&value); err != nil {
			return nil, financial.Failure("INVALID_JSON")
		}
		result[key] = value
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, financial.Failure("INVALID_JSON")
	}
	if _, err = decoder.Token(); err != io.EOF {
		return nil, financial.Failure("INVALID_JSON")
	}
	return result, nil
}
func stringField(obj map[string]json.RawMessage, key string, optional bool) (string, error) {
	raw, ok := obj[key]
	if !ok && optional {
		return "", nil
	}
	if len(raw) == 0 || raw[0] != '"' {
		return "", financial.Failure("INVALID_JSON")
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", financial.Failure("INVALID_JSON")
	}
	if !financial.Text(value, 256) {
		return "", financial.Failure("INVALID_IDENTIFIER")
	}
	return value, nil
}
func moneyField(obj map[string]json.RawMessage, key string) (money.Money, error) {
	m, err := money.ParseJSON(obj[key])
	if err != nil {
		if errors.Is(err, money.ErrInvalidCurrency) {
			return money.Money{}, financial.Failure("UNSUPPORTED_CURRENCY")
		}
		return money.Money{}, financial.Failure("INVALID_MONEY")
	}
	return m, nil
}
func principal(r *http.Request) auth.Principal { p, _ := auth.FromContext(r.Context()); return p }
func (a *FinancialAPI) open(w http.ResponseWriter, r *http.Request) error {
	obj, err := readObject(w, r, "playerId", "initialBalance")
	if err != nil {
		return err
	}
	player, err := stringField(obj, "playerId", false)
	if err != nil {
		return err
	}
	balance, err := moneyField(obj, "initialBalance")
	if err != nil {
		return err
	}
	result, err := a.service.Open(r.Context(), principal(r), player, balance, metadata(r))
	if err != nil {
		return err
	}
	w.Header().Set("Location", "/wallets/"+result.ID)
	return respond(w, 201, result)
}
func (a *FinancialAPI) wallet(w http.ResponseWriter, r *http.Request) error {
	result, err := a.service.Wallet(r.Context(), principal(r), r.PathValue("walletId"))
	if err != nil {
		return err
	}
	return respond(w, 200, result)
}
func (a *FinancialAPI) submit(w http.ResponseWriter, r *http.Request) error {
	keys := r.Header.Values("Idempotency-Key")
	if len(keys) != 1 || !financial.Text(keys[0], 512) {
		return financial.Failure("INVALID_IDEMPOTENCY_KEY")
	}
	obj, err := readObject(w, r, "providerId", "externalTransactionId", "playerId", "walletId", "roundId", "gameId", "kind", "money", "referenceExternalTransactionId")
	if err != nil {
		return err
	}
	fields := make(map[string]string)
	for _, key := range []string{"providerId", "externalTransactionId", "playerId", "walletId", "roundId", "gameId", "kind", "referenceExternalTransactionId"} {
		fields[key], err = stringField(obj, key, key == "referenceExternalTransactionId")
		if err != nil {
			return err
		}
	}
	// Authorization precedes money parsing and any persistent identity lookup.
	if err = principal(r).AuthorizeProvider(fields["providerId"]); err != nil {
		return err
	}
	m, err := moneyField(obj, "money")
	if err != nil {
		return err
	}
	result, err := a.service.Submit(r.Context(), principal(r), tx.ExternalInput{ProviderID: fields["providerId"], ExternalTransactionID: fields["externalTransactionId"], PlayerID: fields["playerId"], WalletID: fields["walletId"], RoundID: fields["roundId"], GameID: fields["gameId"], Kind: tx.Kind(fields["kind"]), Money: m, ReferenceExternalTransactionID: fields["referenceExternalTransactionId"], IdempotencyKey: keys[0]}, metadata(r))
	if err != nil {
		return err
	}
	status := 200
	switch result.Status {
	case tx.Pending, tx.PendingReference:
		status = 202
	case tx.Rejected:
		status = 422
	case tx.Failed:
		status = 500
	}
	return respond(w, status, result)
}
func (a *FinancialAPI) transaction(w http.ResponseWriter, r *http.Request) error {
	result, err := a.service.Transaction(r.Context(), principal(r), r.PathValue("transactionId"))
	if err != nil {
		return err
	}
	return respond(w, 200, result)
}
func (a *FinancialAPI) external(w http.ResponseWriter, r *http.Request) error {
	result, err := a.service.External(r.Context(), principal(r), r.PathValue("providerId"), r.PathValue("externalTransactionId"))
	if err != nil {
		return err
	}
	return respond(w, 200, result)
}
func (a *FinancialAPI) ledger(w http.ResponseWriter, r *http.Request) error {
	query, queryErr := url.ParseQuery(r.URL.RawQuery)
	if queryErr != nil {
		return financial.Failure("INVALID_PAGINATION")
	}
	for key, values := range query {
		if (key != "cursor" && key != "limit") || len(values) != 1 {
			return financial.Failure("INVALID_PAGINATION")
		}
	}
	limit := 50
	if values, ok := query["limit"]; ok {
		parsed, err := strconv.Atoi(values[0])
		if err != nil {
			return financial.Failure("INVALID_PAGINATION")
		}
		limit = parsed
	}
	result, err := a.service.Ledger(r.Context(), principal(r), r.PathValue("walletId"), query.Get("cursor"), limit)
	if err != nil {
		return err
	}
	return respond(w, 200, result)
}
func (a *FinancialAPI) reconcile(w http.ResponseWriter, r *http.Request) error {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1))
	if err != nil || len(data) > 0 {
		return financial.Failure("INVALID_JSON")
	}
	result, err := a.service.Reconcile(r.Context(), principal(r), r.PathValue("walletId"))
	if err != nil {
		return err
	}
	if !result.Consistent {
		a.logger.ErrorContext(r.Context(), "wallet reconciliation mismatch", "walletId", result.WalletID, "correlationId", metadata(r).CorrelationID)
	}
	return respond(w, 200, result)
}
func (a *FinancialAPI) metrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = fmt.Fprintf(w, "# HELP wagering_reconciliation_mismatches_total Detected wallet reconciliation mismatches.\n# TYPE wagering_reconciliation_mismatches_total counter\nwagering_reconciliation_mismatches_total %d\n", a.service.ReconciliationMismatches())
}
