package plaid

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jmoneytech-stack/moneta/internal/secret"
	"github.com/jmoneytech-stack/moneta/internal/store"
)

func TestLinkServerUsesLoopbackAndCompletesSameOriginSession(t *testing.T) {
	backend := &fakeLinkBackend{
		linkToken: fakeLinkToken,
		linkedItem: LinkedItem{
			DatabaseID:  17,
			ItemID:      "item-fake",
			Institution: "Sandbox Bank",
		},
	}
	var logs bytes.Buffer
	server, err := newLinkServer(
		backend,
		LinkServerConfig{Logger: log.New(&logs, "", 0)},
		bytes.NewReader(bytes.Repeat([]byte{3}, 64)),
	)
	if err != nil {
		t.Fatalf("newLinkServer() error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session, err := server.Start(ctx)
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	t.Cleanup(func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
		defer closeCancel()
		_ = session.Close(closeCtx)
	})

	parsedURL, err := url.Parse(session.URL)
	if err != nil {
		t.Fatalf("parse session URL: %v", err)
	}
	if parsedURL.Hostname() != "127.0.0.1" || parsedURL.Port() == "" {
		t.Fatalf("session URL = %q, want explicit 127.0.0.1 port", session.URL)
	}
	if !strings.Contains(logs.String(), "Plaid Link listening on "+session.URL) {
		t.Fatalf("startup log = %q, want bound address", logs.String())
	}
	if strings.Contains(logs.String(), fakeLinkToken) || strings.Contains(logs.String(), fakePublicToken) {
		t.Fatal("startup log contains a Plaid token")
	}

	response, err := http.Get(session.URL)
	if err != nil {
		t.Fatalf("GET Link page: %v", err)
	}
	page, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatalf("read Link page: %v", err)
	}
	if response.StatusCode != http.StatusOK || !bytes.Contains(page, []byte(fakeLinkToken)) {
		t.Fatalf("Link page status/body = %d/%q", response.StatusCode, page)
	}
	for header, want := range map[string]string{
		"Cache-Control":           "no-store",
		"Referrer-Policy":         "no-referrer",
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
		"Content-Security-Policy": "frame-ancestors 'none'",
	} {
		if !strings.Contains(response.Header.Get(header), want) {
			t.Errorf("%s = %q, want %q", header, response.Header.Get(header), want)
		}
	}
	sessionTokenMatch := regexp.MustCompile(`const sessionToken = "([^"]+)"`).FindSubmatch(page)
	if len(sessionTokenMatch) != 2 {
		t.Fatalf("Link page does not contain a session token: %q", page)
	}
	sessionToken := string(sessionTokenMatch[1])

	postBody, err := json.Marshal(completeLinkRequest{
		PublicToken: fakePublicToken,
		Institution: "Sandbox Bank",
	})
	if err != nil {
		t.Fatalf("encode completion request: %v", err)
	}

	wrongOriginRequest, err := http.NewRequest(
		http.MethodPost,
		session.URL+"/complete",
		bytes.NewReader(postBody),
	)
	if err != nil {
		t.Fatalf("create wrong-origin request: %v", err)
	}
	wrongOriginRequest.Header.Set("Content-Type", "application/json")
	wrongOriginRequest.Header.Set("Origin", "https://example.invalid")
	wrongOriginRequest.Header.Set(linkSessionHeader, sessionToken)
	wrongOriginResponse, err := http.DefaultClient.Do(wrongOriginRequest)
	if err != nil {
		t.Fatalf("send wrong-origin request: %v", err)
	}
	wrongOriginResponse.Body.Close()
	if wrongOriginResponse.StatusCode != http.StatusForbidden || backend.completeCalls != 0 {
		t.Fatalf("wrong-origin status/calls = %d/%d", wrongOriginResponse.StatusCode, backend.completeCalls)
	}

	missingSessionRequest, err := http.NewRequest(
		http.MethodPost,
		session.URL+"/complete",
		bytes.NewReader(postBody),
	)
	if err != nil {
		t.Fatalf("create missing-session request: %v", err)
	}
	missingSessionRequest.Header.Set("Content-Type", "application/json")
	missingSessionRequest.Header.Set("Origin", session.URL)
	missingSessionResponse, err := http.DefaultClient.Do(missingSessionRequest)
	if err != nil {
		t.Fatalf("send missing-session request: %v", err)
	}
	missingSessionResponse.Body.Close()
	if missingSessionResponse.StatusCode != http.StatusForbidden || backend.completeCalls != 0 {
		t.Fatalf("missing-session status/calls = %d/%d", missingSessionResponse.StatusCode, backend.completeCalls)
	}

	wrongHostRequest, err := http.NewRequest(http.MethodGet, session.URL, nil)
	if err != nil {
		t.Fatalf("create wrong-host request: %v", err)
	}
	wrongHostRequest.Host = "localhost:" + parsedURL.Port()
	wrongHostResponse, err := http.DefaultClient.Do(wrongHostRequest)
	if err != nil {
		t.Fatalf("send wrong-host request: %v", err)
	}
	wrongHostResponse.Body.Close()
	if wrongHostResponse.StatusCode != http.StatusMisdirectedRequest {
		t.Fatalf("wrong-host status = %d", wrongHostResponse.StatusCode)
	}

	request, err := http.NewRequest(
		http.MethodPost,
		session.URL+"/complete",
		bytes.NewReader(postBody),
	)
	if err != nil {
		t.Fatalf("create completion request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	request.Header.Set("Origin", session.URL)
	request.Header.Set(linkSessionHeader, sessionToken)
	completionResponse, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("complete Link session: %v", err)
	}
	completionBody, err := io.ReadAll(completionResponse.Body)
	completionResponse.Body.Close()
	if err != nil {
		t.Fatalf("read completion response: %v", err)
	}
	if completionResponse.StatusCode != http.StatusCreated {
		t.Fatalf("completion status/body = %d/%q", completionResponse.StatusCode, completionBody)
	}
	if bytes.Contains(completionBody, []byte(fakePublicToken)) ||
		bytes.Contains(completionBody, []byte(fakePermanentToken)) {
		t.Fatal("completion response contains a Plaid token")
	}
	if backend.publicToken != fakePublicToken || backend.institution != "Sandbox Bank" {
		t.Errorf("backend completion input = %q/%q", backend.publicToken, backend.institution)
	}

	waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
	defer waitCancel()
	linked, err := session.Wait(waitCtx)
	if err != nil {
		t.Fatalf("Wait() error: %v", err)
	}
	if linked != backend.linkedItem {
		t.Errorf("Wait() = %#v, want %#v", linked, backend.linkedItem)
	}
}

func TestLinkServerRejectsEveryNonLoopbackBind(t *testing.T) {
	addresses := []string{
		"0.0.0.0:0",
		":0",
		"localhost:0",
		"127.0.0.2:0",
		"[::1]:0",
		"127.0.0.1:not-a-port",
	}
	for _, address := range addresses {
		t.Run(address, func(t *testing.T) {
			backend := &fakeLinkBackend{linkToken: fakeLinkToken}
			_, err := newLinkServer(
				backend,
				LinkServerConfig{ListenAddress: address, Logger: log.New(io.Discard, "", 0)},
				bytes.NewReader(bytes.Repeat([]byte{1}, 64)),
			)
			if err == nil {
				t.Fatal("newLinkServer() accepted a non-loopback bind")
			}
			if backend.createCalls != 0 {
				t.Fatal("invalid bind contacted Plaid")
			}
		})
	}
}

func TestLinkServerDoesNotLogOrReturnBackendSecretsOnFailure(t *testing.T) {
	backend := &fakeLinkBackend{
		linkToken:   fakeLinkToken,
		completeErr: &APIError{Code: "INVALID_PUBLIC_TOKEN"},
	}
	var logs bytes.Buffer
	server, err := newLinkServer(
		backend,
		LinkServerConfig{Logger: log.New(&logs, "", 0)},
		bytes.NewReader(bytes.Repeat([]byte{5}, 64)),
	)
	if err != nil {
		t.Fatalf("newLinkServer() error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session, err := server.Start(ctx)
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
		defer closeCancel()
		_ = session.Close(closeCtx)
	}()

	pageResponse, err := http.Get(session.URL)
	if err != nil {
		t.Fatalf("GET Link page: %v", err)
	}
	page, _ := io.ReadAll(pageResponse.Body)
	pageResponse.Body.Close()
	sessionToken := regexp.MustCompile(`const sessionToken = "([^"]+)"`).FindSubmatch(page)[1]
	body := `{"public_token":"` + fakePublicToken + `","institution":"Sandbox Bank"}`
	request, err := http.NewRequest(http.MethodPost, session.URL+"/complete", strings.NewReader(body))
	if err != nil {
		t.Fatalf("create completion request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", session.URL)
	request.Header.Set(linkSessionHeader, string(sessionToken))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("send completion request: %v", err)
	}
	responseBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("completion status = %d, want 502", response.StatusCode)
	}
	for _, secretValue := range []string{fakeLinkToken, fakePublicToken, "INVALID_PUBLIC_TOKEN"} {
		if bytes.Contains(responseBody, []byte(secretValue)) || strings.Contains(logs.String(), secretValue) {
			t.Fatalf("failure output contains %q", secretValue)
		}
	}
}

func TestLinkServerPersistsItemAfterClientDisconnectFollowingExchange(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "moneta.db"))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close test database: %v", err)
		}
	})
	cipher, err := secret.NewCipher(bytes.Repeat([]byte{11}, 32))
	if err != nil {
		t.Fatalf("create test cipher: %v", err)
	}
	gateway := &disconnectAfterExchangeGateway{
		exchangeSucceeded: make(chan struct{}),
	}
	linker, err := newLinker(gateway, db, cipher)
	if err != nil {
		t.Fatalf("newLinker() error: %v", err)
	}
	var logs bytes.Buffer
	server, err := newLinkServer(
		linker,
		LinkServerConfig{Logger: log.New(&logs, "", 0)},
		bytes.NewReader(bytes.Repeat([]byte{7}, 64)),
	)
	if err != nil {
		t.Fatalf("newLinkServer() error: %v", err)
	}

	// Hold SQLite's only connection so the save cannot finish before the
	// simulated disconnect. A database waiter proves the exchange returned and
	// persistence reached the exchange-to-save window before cancellation.
	heldConnection, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("hold database connection: %v", err)
	}
	connectionReleased := false
	t.Cleanup(func() {
		if !connectionReleased {
			_ = heldConnection.Close()
		}
	})

	body, err := json.Marshal(completeLinkRequest{
		PublicToken: fakePublicToken,
		Institution: "Test Bank",
	})
	if err != nil {
		t.Fatalf("encode completion request: %v", err)
	}
	requestContext, cancelRequest := context.WithCancel(context.Background())
	defer cancelRequest()
	request := httptest.NewRequest(
		http.MethodPost,
		"http://127.0.0.1:12345/complete",
		bytes.NewReader(body),
	).WithContext(requestContext)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://127.0.0.1:12345")
	request.Header.Set(linkSessionHeader, "test-session-token")
	recorder := httptest.NewRecorder()
	session := &LinkSession{
		server: &http.Server{},
		done:   make(chan struct{}),
	}

	handlerDone := make(chan struct{})
	go func() {
		defer close(handlerDone)
		server.completeLink(
			session,
			"http://127.0.0.1:12345",
			"test-session-token",
			recorder,
			request,
		)
	}()

	select {
	case <-gateway.exchangeSucceeded:
	case <-time.After(time.Second):
		t.Fatal("public token exchange did not complete")
	}
	deadline := time.Now().Add(time.Second)
	for db.Stats().WaitCount == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if db.Stats().WaitCount == 0 {
		t.Fatal("Item persistence did not wait for the held database connection")
	}

	cancelRequest()
	if err := heldConnection.Close(); err != nil {
		t.Fatalf("release database connection: %v", err)
	}
	connectionReleased = true

	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("Link completion did not return after client disconnect")
	}
	if gateway.exchangeCalls != 1 {
		t.Fatalf("public token exchange calls = %d, want 1", gateway.exchangeCalls)
	}

	var itemCount int
	if err := db.QueryRow("SELECT count(*) FROM provider_items").Scan(&itemCount); err != nil {
		t.Fatalf("count stored provider Items: %v", err)
	}
	if itemCount != 1 {
		t.Errorf("stored provider Item count = %d, want 1", itemCount)
	}
	if recorder.Code != http.StatusCreated {
		t.Errorf("completion status = %d, want %d", recorder.Code, http.StatusCreated)
	}
	for _, token := range []string{fakePublicToken, fakePermanentToken} {
		if strings.Contains(logs.String(), token) || strings.Contains(recorder.Body.String(), token) {
			t.Fatalf("Link completion output contains token material")
		}
	}
}

type fakeLinkBackend struct {
	linkToken     string
	createErr     error
	createCalls   int
	linkedItem    LinkedItem
	completeErr   error
	completeCalls int
	publicToken   string
	institution   string
}

func (b *fakeLinkBackend) CreateLinkToken(context.Context) (string, error) {
	b.createCalls++
	return b.linkToken, b.createErr
}

func (b *fakeLinkBackend) CompleteLink(
	_ context.Context,
	publicToken string,
	institution string,
) (LinkedItem, error) {
	b.completeCalls++
	b.publicToken = publicToken
	b.institution = institution
	return b.linkedItem, b.completeErr
}

type disconnectAfterExchangeGateway struct {
	exchangeSucceeded chan struct{}
	exchangeCalls     int
}

func (g *disconnectAfterExchangeGateway) createLinkToken(
	context.Context,
	string,
) (string, error) {
	return fakeLinkToken, nil
}

func (g *disconnectAfterExchangeGateway) exchangePublicToken(
	context.Context,
	string,
) (rawLinkExchange, error) {
	g.exchangeCalls++
	close(g.exchangeSucceeded)
	return rawLinkExchange{
		ItemID:      "item-fake",
		AccessToken: fakePermanentToken,
	}, nil
}
