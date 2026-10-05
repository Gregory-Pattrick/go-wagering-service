package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/config"

	"go.uber.org/fx"
)

func NewRouter(logger *slog.Logger) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		response := struct {
			Status string `json:"status"`
		}{
			Status: "ok",
		}

		if err := json.NewEncoder(w).Encode(response); err != nil {
			logger.ErrorContext(r.Context(), "write liveness response", "error", err)
		}
	})

	return mux
}

func NewServer(
	lifecycle fx.Lifecycle,
	cfg config.Config,
	router *http.ServeMux,
	authentication *Authentication,
	logger *slog.Logger,
	shutdowner fx.Shutdowner,
) *http.Server {
	server := &http.Server{
		Addr:              cfg.HTTPAddress,
		Handler:           authentication.Protect(router),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}

	done := make(chan struct{})

	lifecycle.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			listenConfig := net.ListenConfig{}

			listener, err := listenConfig.Listen(ctx, "tcp", server.Addr)
			if err != nil {
				return fmt.Errorf("start HTTP listener: %w", err)
			}

			// Record the actual address, including an OS-assigned test port.
			server.Addr = listener.Addr().String()

			go func() {
				defer close(done)

				err := server.Serve(listener)
				if err != nil && !errors.Is(err, http.ErrServerClosed) {
					logger.Error("HTTP server failed", "error", err)

					if shutdownErr := shutdowner.Shutdown(fx.ExitCode(1)); shutdownErr != nil {
						logger.Error("request application shutdown", "error", shutdownErr)
					}
				}
			}()

			logger.InfoContext(ctx, "HTTP server listening", "address", server.Addr)
			return nil
		},
		OnStop: func(ctx context.Context) error {
			logger.InfoContext(ctx, "HTTP server stopping")

			shutdownErr := server.Shutdown(ctx)
			if shutdownErr != nil {
				shutdownErr = errors.Join(shutdownErr, server.Close())
			}

			select {
			case <-done:
				return shutdownErr
			case <-ctx.Done():
				return errors.Join(shutdownErr, ctx.Err())
			}
		},
	})

	return server
}
