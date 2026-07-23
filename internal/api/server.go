// Package api exposes Moneta's read-only store queries over authenticated
// loopback HTTP. It contains no provider or credential access.
package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const shutdownTimeout = 5 * time.Second

type server struct {
	db         *sql.DB
	apiKeyHash [sha256.Size]byte
	logger     *log.Logger
	now        func() time.Time
}

// NewHandler returns the authenticated read-only REST handler. The API key is
// hashed immediately and is never logged or included in an error response.
func NewHandler(db *sql.DB, apiKey string, logger *log.Logger) (http.Handler, error) {
	if db == nil {
		return nil, fmt.Errorf("database is required")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("API key is required")
	}
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	s := &server{
		db:         db,
		apiKeyHash: sha256.Sum256([]byte(apiKey)),
		logger:     logger,
		now:        time.Now,
	}
	mux := http.NewServeMux()
	routes := []struct {
		path    string
		handler http.HandlerFunc
	}{
		{"/v1/status", s.handleStatus},
		{"/v1/accounts", s.handleAccounts},
		{"/v1/transactions", s.handleTransactions},
		{"/v1/spend", s.handleSpend},
		{"/v1/cashflow", s.handleCashflow},
		{"/v1/networth", s.handleNetworth},
		{"/v1/debts", s.handleDebts},
		{"/v1/trends", s.handleTrends},
	}
	for _, route := range routes {
		mux.Handle("GET "+route.path, route.handler)
		mux.HandleFunc(route.path, func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Allow", "GET, HEAD")
			writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
		})
	}
	mux.HandleFunc("/", func(writer http.ResponseWriter, _ *http.Request) {
		writeError(writer, http.StatusNotFound, "not found")
	})
	return s.authenticate(s.recoverPanics(mux)), nil
}

func (s *server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		provided := sha256.Sum256([]byte(request.Header.Get("X-API-Key")))
		if subtle.ConstantTimeCompare(provided[:], s.apiKeyHash[:]) != 1 {
			writeError(writer, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func (s *server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.logger.Printf("REST handler panic: %v", recovered)
				writeError(writer, http.StatusInternalServerError, "internal server error")
			}
		}()
		next.ServeHTTP(writer, request)
	})
}

// ValidateListenAddress validates host:port syntax and returns whether the
// host is loopback. Non-loopback hosts require allowNonLoopback. A blank host
// (for example :8080) is treated as non-loopback because Go binds it broadly.
func ValidateListenAddress(address string, allowNonLoopback bool) (bool, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return false, fmt.Errorf("--listen must use host:port form: %w", err)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 0 || portNumber > 65535 {
		return false, fmt.Errorf("--listen port must be an integer from 0 to 65535")
	}

	loopback := strings.EqualFold(host, "localhost")
	if ip := net.ParseIP(host); ip != nil {
		loopback = ip.IsLoopback()
	}
	if !loopback && !allowNonLoopback {
		return false, fmt.Errorf(
			"non-loopback --listen address %q requires --allow-non-loopback",
			address,
		)
	}
	return loopback, nil
}

// Serve serves handler on an already-bound listener until the context is
// canceled or the server fails. Context cancellation performs a bounded
// graceful shutdown and returns nil on a clean stop.
func Serve(ctx context.Context, listener net.Listener, handler http.Handler) error {
	if listener == nil {
		return fmt.Errorf("listener is required")
	}
	if handler == nil {
		return fmt.Errorf("handler is required")
	}
	httpServer := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- httpServer.Serve(listener)
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shut down REST server: %w", err)
		}
		err := <-errCh
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}
