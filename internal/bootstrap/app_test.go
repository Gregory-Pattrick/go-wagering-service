package bootstrap

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/adapters/oidcauth"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/adapters/postgres"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/config"

	"go.uber.org/fx"
)

func TestApplicationServesLivenessAndReleasesListener(t *testing.T) {
	t.Setenv("HTTP_ADDR", "127.0.0.1:8080")
	t.Setenv("LOG_LEVEL", "INFO")

	var server *http.Server

	app := New(
		fx.Replace(&oidcauth.Verifier{}),
		fx.Replace(&postgres.Database{}),
		fx.Decorate(func(cfg config.Config) config.Config {
			cfg.HTTPAddress = "127.0.0.1:0"
			return cfg
		}),
		fx.Populate(&server),
	)

	if err := app.Err(); err != nil {
		t.Fatalf("compose application: %v", err)
	}

	t.Cleanup(func() {
		stopApplication(t, app)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := app.Start(ctx); err != nil {
		t.Fatalf("start application: %v", err)
	}

	transport := &http.Transport{}
	client := &http.Client{
		Transport: transport,
		Timeout:   3 * time.Second,
	}
	defer transport.CloseIdleConnections()

	response, err := client.Get("http://" + server.Addr + "/health/live")
	if err != nil {
		t.Fatalf("request liveness: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status: %d", response.StatusCode)
	}

	if got := response.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("unexpected content type: %s", got)
	}

	var body struct {
		Status string `json:"status"`
	}

	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode liveness response: %v", err)
	}

	if body.Status != "ok" {
		t.Errorf("unexpected liveness status: %s", body.Status)
	}

	response.Body.Close()

	protectedResponse, err := client.Get("http://" + server.Addr + "/wallets")
	if err != nil {
		t.Fatalf("request protected path: %v", err)
	}
	protectedResponse.Body.Close()
	if protectedResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("protected path returned %d without credentials", protectedResponse.StatusCode)
	}

	transport.CloseIdleConnections()

	stopApplication(t, app)

	connection, err := net.DialTimeout("tcp", server.Addr, time.Second)
	if err == nil {
		connection.Close()
		t.Fatal("HTTP listener remained reachable after shutdown")
	}
}

func TestApplicationRejectsOccupiedPort(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer listener.Close()

	t.Setenv("HTTP_ADDR", listener.Addr().String())
	t.Setenv("LOG_LEVEL", "INFO")

	app := New(fx.Replace(&postgres.Database{}, &oidcauth.Verifier{}))

	if err := app.Err(); err != nil {
		t.Fatalf("compose application: %v", err)
	}

	t.Cleanup(func() {
		stopApplication(t, app)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := app.Start(ctx); err == nil {
		t.Fatal("expected startup failure for an occupied port")
	}
}

func TestApplicationRejectsInvalidConfiguration(t *testing.T) {
	t.Setenv("HTTP_ADDR", "127.0.0.1:8080")
	t.Setenv("LOG_LEVEL", "INVALID")

	app := New()

	if err := app.Err(); err == nil {
		t.Fatal("expected application composition to reject invalid configuration")
	}
}

func stopApplication(t *testing.T, app *fx.App) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := app.Stop(ctx); err != nil {
		t.Errorf("stop application: %v", err)
	}
}
