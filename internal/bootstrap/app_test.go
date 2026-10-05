package bootstrap

import (
	"context"
	"testing"
	"time"
)

func TestApplicationStartsAndStops(t *testing.T) {
	t.Setenv("HTTP_ADDR", "127.0.0.1:8080")
	t.Setenv("LOG_LEVEL", "INFO")

	app := New()

	if err := app.Err(); err != nil {
		t.Fatalf("compose application: %v", err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := app.Stop(ctx); err != nil {
			t.Errorf("stop application: %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := app.Start(ctx); err != nil {
		t.Fatalf("start application: %v", err)
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
