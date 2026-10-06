package httpapi

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/application/financial"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/auth"
)

func TestStrictFinancialJSON(t *testing.T) {
	for _, body := range []string{`null`, `[]`, `{}`, `{"playerId":"a","playerId":"b","initialBalance":{}}`, `{"PlayerId":"a","initialBalance":{}}`, `{"playerId":"a","initialBalance":{},"extra":1}`, `{"playerId":"a","initialBalance":{}} {}`} {
		request := httptest.NewRequest("POST", "/wallets", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		obj, err := readObject(httptest.NewRecorder(), request, "playerId", "initialBalance")
		if body == `{}` {
			if err != nil {
				t.Fatal(err)
			}
			if _, err = stringField(obj, "playerId", false); err == nil {
				t.Fatal("missing required field accepted")
			}
		} else if err == nil {
			t.Fatalf("invalid JSON accepted: %s", body)
		}
	}
	for _, raw := range []string{`{"amount":1,"currency":"BRL"}`, `{"amount":"1.00","amount":"2.00","currency":"BRL"}`, `{"amount":"1.0","currency":"BRL"}`, `{"amount":"1.00","currency":"EUR"}`} {
		request := httptest.NewRequest("POST", "/wallets", strings.NewReader(`{"playerId":"a","initialBalance":`+raw+`}`))
		request.Header.Set("Content-Type", "application/json")
		obj, err := readObject(httptest.NewRecorder(), request, "playerId", "initialBalance")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = moneyField(obj, "initialBalance"); err == nil {
			t.Fatal("invalid money accepted")
		}
	}
	request := httptest.NewRequest("POST", "/wallets", strings.NewReader(strings.Repeat("x", 65537)))
	request.Header.Set("Content-Type", "application/json")
	_, err := readObject(httptest.NewRecorder(), request, "playerId")
	var coded *financial.Error
	if !errors.As(err, &coded) || coded.Code != "PAYLOAD_TOO_LARGE" {
		t.Fatal(err)
	}
}
func TestFinancialRouteAuthorizationAndUnavailableResponse(t *testing.T) {
	accesses := 0
	service := financial.New(func() (financial.Backend, error) { accesses++; return nil, errors.New("private database details") })
	api := NewFinancialAPI(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	mux := http.NewServeMux()
	RegisterFinancialRoutes(mux, api)
	provider, _ := auth.NewProvider("provider-a", "provider-a")
	internal, _ := auth.NewInternal("wallet-service")
	for _, test := range []struct {
		method, path string
		p            auth.Principal
		want         int
	}{
		{"GET", "/wallets/0192f291-27dd-7d3f-8071-5f8685deef37", provider, 403},
		{"POST", "/wallets", provider, 403},
		{"GET", "/wagering/transactions/0192f291-27dd-7d3f-8071-5f8685deef37", internal, 403},
		{"GET", "/metrics", provider, 403},
		{"GET", "/wallets/0192f291-27dd-7d3f-8071-5f8685deef37", auth.Principal{}, 401},
	} {
		req := httptest.NewRequest(test.method, test.path, nil)
		req = req.WithContext(auth.WithPrincipal(req.Context(), test.p))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != test.want {
			t.Fatalf("%s: %d", test.path, w.Code)
		}
	}
	if accesses != 0 {
		t.Fatal("unauthorized route touched database")
	}
	req := httptest.NewRequest("GET", "/wallets/0192f291-27dd-7d3f-8071-5f8685deef37", nil)
	req = req.WithContext(auth.WithPrincipal(req.Context(), internal))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 503 || strings.Contains(w.Body.String(), "private") || !strings.Contains(w.Body.String(), `"retryable":true`) {
		t.Fatal(w.Body.String())
	}
}
