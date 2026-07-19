package plaid

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultLinkListenAddress = "127.0.0.1:0"
	linkSessionHeader        = "X-Moneta-Link-Session"
	maxLinkRequestBytes      = 8 << 10
)

var ErrLinkSessionClosed = errors.New("Plaid Link session closed")

//go:embed link.html
var linkPageSource string

var linkPageTemplate = template.Must(template.New("link.html").Parse(linkPageSource))

type linkBackend interface {
	CreateLinkToken(ctx context.Context) (string, error)
	CompleteLink(ctx context.Context, publicToken, institution string) (LinkedItem, error)
}

// LinkServerConfig configures the short-lived Plaid Link HTTP server. An empty
// address selects an ephemeral port on 127.0.0.1. Broader binds are rejected.
type LinkServerConfig struct {
	ListenAddress string
	Logger        *log.Logger
}

// LinkServer serves the embedded Plaid Link page over loopback.
type LinkServer struct {
	backend       linkBackend
	listenAddress string
	logger        *log.Logger
	random        io.Reader
}

// NewLinkServer creates a loopback-only server for a Plaid Linker.
func NewLinkServer(linker *Linker, config LinkServerConfig) (*LinkServer, error) {
	if linker == nil {
		return nil, fmt.Errorf("Plaid Linker is required")
	}
	return newLinkServer(linker, config, rand.Reader)
}

func newLinkServer(
	backend linkBackend,
	config LinkServerConfig,
	random io.Reader,
) (*LinkServer, error) {
	if backend == nil {
		return nil, fmt.Errorf("Plaid Link backend is required")
	}
	listenAddress := config.ListenAddress
	if listenAddress == "" {
		listenAddress = defaultLinkListenAddress
	}
	if err := validateLinkListenAddress(listenAddress); err != nil {
		return nil, err
	}
	logger := config.Logger
	if logger == nil {
		logger = log.Default()
	}
	if random == nil {
		return nil, fmt.Errorf("secure random source is required")
	}
	return &LinkServer{
		backend:       backend,
		listenAddress: listenAddress,
		logger:        logger,
		random:        random,
	}, nil
}

// LinkSession is one running loopback Link flow.
type LinkSession struct {
	URL string

	server       *http.Server
	done         chan struct{}
	finishOnce   sync.Once
	resultMu     sync.RWMutex
	result       linkResult
	completionMu sync.Mutex
	completing   bool
	completed    bool
}

type linkResult struct {
	item LinkedItem
	err  error
}

// Start creates a Plaid link token and starts listening explicitly on
// 127.0.0.1. The actual bound address is logged for exposure auditing.
func (s *LinkServer) Start(ctx context.Context) (*LinkSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	linkToken, err := s.backend.CreateLinkToken(ctx)
	if err != nil {
		return nil, err
	}
	sessionToken, err := randomURLToken(s.random)
	if err != nil {
		return nil, fmt.Errorf("create Link session token: %w", err)
	}
	cspNonce, err := randomURLToken(s.random)
	if err != nil {
		return nil, fmt.Errorf("create Link page nonce: %w", err)
	}

	listener, err := net.Listen("tcp4", s.listenAddress)
	if err != nil {
		return nil, fmt.Errorf("listen for Plaid Link on loopback: %w", err)
	}
	boundAddress := listener.Addr().String()
	url := "http://" + boundAddress
	session := &LinkSession{
		URL:  url,
		done: make(chan struct{}),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(writer http.ResponseWriter, request *http.Request) {
		s.serveLinkPage(writer, linkPageData{
			LinkToken:    linkToken,
			SessionToken: sessionToken,
			CSPNonce:     cspNonce,
		})
	})
	mux.HandleFunc("POST /complete", func(writer http.ResponseWriter, request *http.Request) {
		s.completeLink(session, url, sessionToken, writer, request)
	})

	server := &http.Server{
		Handler:           secureLinkHandler(boundAddress, mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      45 * time.Second,
		IdleTimeout:       30 * time.Second,
		ErrorLog:          s.logger,
	}
	session.server = server
	s.logger.Printf("Plaid Link listening on %s", url)

	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			session.finish(LinkedItem{}, fmt.Errorf("serve Plaid Link: %w", err))
		}
	}()
	go func() {
		select {
		case <-ctx.Done():
			session.finish(LinkedItem{}, ctx.Err())
			_ = server.Close()
		case <-session.done:
		}
	}()

	return session, nil
}

type linkPageData struct {
	LinkToken    string
	SessionToken string
	CSPNonce     string
}

func (s *LinkServer) serveLinkPage(writer http.ResponseWriter, data linkPageData) {
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Content-Security-Policy", strings.Join([]string{
		"default-src 'none'",
		"script-src 'nonce-" + data.CSPNonce + "' https://cdn.plaid.com",
		"style-src 'nonce-" + data.CSPNonce + "'",
		"style-src-elem 'nonce-" + data.CSPNonce + "'",
		"style-src-attr 'unsafe-inline'",
		"connect-src 'self' https://sandbox.plaid.com https://production.plaid.com",
		"frame-src https://cdn.plaid.com",
		"img-src data: https://*.plaid.com",
		"base-uri 'none'",
		"form-action 'none'",
		"frame-ancestors 'none'",
	}, "; "))
	if err := linkPageTemplate.Execute(writer, data); err != nil {
		s.logger.Printf("Plaid Link page rendering failed")
	}
}

type completeLinkRequest struct {
	PublicToken string `json:"public_token"`
	Institution string `json:"institution"`
}

func (s *LinkServer) completeLink(
	session *LinkSession,
	origin string,
	sessionToken string,
	writer http.ResponseWriter,
	request *http.Request,
) {
	if request.Header.Get("Origin") != origin {
		writeLinkError(writer, http.StatusForbidden, "request origin is not allowed")
		return
	}
	if request.Header.Get(linkSessionHeader) != sessionToken {
		writeLinkError(writer, http.StatusForbidden, "Link session is invalid")
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeLinkError(writer, http.StatusUnsupportedMediaType, "content type must be application/json")
		return
	}
	if !session.beginCompletion() {
		writeLinkError(writer, http.StatusConflict, "Link session is already completing")
		return
	}

	request.Body = http.MaxBytesReader(writer, request.Body, maxLinkRequestBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var input completeLinkRequest
	if err := decoder.Decode(&input); err != nil {
		session.endCompletion(false)
		writeLinkError(writer, http.StatusBadRequest, "request body is invalid")
		return
	}
	if err := ensureJSONEnd(decoder); err != nil {
		session.endCompletion(false)
		writeLinkError(writer, http.StatusBadRequest, "request body is invalid")
		return
	}

	completionCtx, cancel := context.WithTimeout(
		context.WithoutCancel(request.Context()),
		30*time.Second,
	)
	defer cancel()

	item, err := s.backend.CompleteLink(
		completionCtx,
		input.PublicToken,
		input.Institution,
	)
	if err != nil {
		session.endCompletion(false)
		s.logger.Printf("Plaid Link completion failed")
		writeLinkError(writer, http.StatusBadGateway, "Link completion failed")
		return
	}
	session.endCompletion(true)

	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(writer).Encode(map[string]any{
		"status":      "linked",
		"database_id": item.DatabaseID,
		"item_id":     item.ItemID,
		"institution": item.Institution,
	})
	session.finish(item, nil)
	go session.shutdownAfterCompletion()
}

func secureLinkHandler(expectedHost string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("X-Frame-Options", "DENY")
		if request.Host != expectedHost {
			writeLinkError(writer, http.StatusMisdirectedRequest, "request host is not allowed")
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func writeLinkError(writer http.ResponseWriter, status int, message string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]string{"error": message})
}

func ensureJSONEnd(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func (s *LinkSession) beginCompletion() bool {
	s.completionMu.Lock()
	defer s.completionMu.Unlock()
	if s.completing || s.completed {
		return false
	}
	s.completing = true
	return true
}

func (s *LinkSession) endCompletion(completed bool) {
	s.completionMu.Lock()
	defer s.completionMu.Unlock()
	s.completing = false
	if completed {
		s.completed = true
	}
}

func (s *LinkSession) finish(item LinkedItem, err error) {
	s.finishOnce.Do(func() {
		s.resultMu.Lock()
		s.result = linkResult{item: item, err: err}
		s.resultMu.Unlock()
		close(s.done)
	})
}

func (s *LinkSession) shutdownAfterCompletion() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.server.Shutdown(ctx)
}

// Wait blocks until the Item is linked, the server fails, or ctx ends.
func (s *LinkSession) Wait(ctx context.Context) (LinkedItem, error) {
	select {
	case <-s.done:
		s.resultMu.RLock()
		result := s.result
		s.resultMu.RUnlock()
		return result.item, result.err
	case <-ctx.Done():
		return LinkedItem{}, ctx.Err()
	}
}

// Close stops the loopback server. It does not remove a successfully linked
// Item.
func (s *LinkSession) Close(ctx context.Context) error {
	err := s.server.Shutdown(ctx)
	s.finish(LinkedItem{}, ErrLinkSessionClosed)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func validateLinkListenAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" {
		return fmt.Errorf("Plaid Link must listen on 127.0.0.1:<port>")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 0 || portNumber > 65535 {
		return fmt.Errorf("Plaid Link port is invalid")
	}
	return nil
}

func randomURLToken(random io.Reader) (string, error) {
	bytes := make([]byte, 32)
	if _, err := io.ReadFull(random, bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}
