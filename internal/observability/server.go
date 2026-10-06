package observability

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/config"
	"go.uber.org/fx"
)

type Server struct{ HTTP *http.Server }

func NewServer(lifecycle fx.Lifecycle, c config.TelemetryConfig, h *Health, r *Registry, logger *slog.Logger, shutdown fx.Shutdowner) *Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodHead {
			_, _ = w.Write([]byte("{\"status\":\"ok\"}\n"))
		}
	})
	mux.HandleFunc("GET /health/ready", h.Ready)
	mux.Handle("GET /metrics", r.Handler())
	server := &http.Server{Addr: c.Address, Handler: mux, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	done := make(chan struct{})
	lifecycle.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			var lc net.ListenConfig
			listener, err := lc.Listen(ctx, "tcp", server.Addr)
			if err != nil {
				return err
			}
			server.Addr = listener.Addr().String()
			go func() {
				defer close(done)
				if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
					logger.Error("operations listener failed")
					_ = shutdown.Shutdown(fx.ExitCode(1))
				}
			}()
			logger.Info("operations listener started", "address", server.Addr)
			return nil
		},
		OnStop: func(ctx context.Context) error {
			err := server.Shutdown(ctx)
			if err != nil {
				err = errors.Join(err, server.Close())
			}
			select {
			case <-done:
				return err
			case <-ctx.Done():
				return errors.Join(err, ctx.Err())
			}
		},
	})
	return &Server{server}
}
