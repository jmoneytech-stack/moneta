package plaid

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestSDKGatewayBuildsRequestsAndParsesResponses(t *testing.T) {
	var requestedPaths []string
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestedPaths = append(requestedPaths, request.URL.Path)
		if request.Header.Get("PLAID-CLIENT-ID") != fakeClientID {
			t.Error("request is missing Plaid client ID header")
		}
		if request.Header.Get("PLAID-SECRET") != fakeSecret {
			t.Error("request is missing Plaid secret header")
		}

		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		var fields map[string]any
		if err := json.Unmarshal(body, &fields); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if fields["access_token"] != fakeAccessToken {
			t.Error("request body is missing access token")
		}

		switch request.URL.Path {
		case "/transactions/sync":
			if fields["cursor"] != "cursor-1" || fields["count"] != float64(syncPageSize) {
				t.Errorf("sync cursor/count = %v/%v", fields["cursor"], fields["count"])
			}
			options, ok := fields["options"].(map[string]any)
			if !ok {
				t.Fatalf("sync options = %#v", fields["options"])
			}
			if options["include_original_description"] != true ||
				options["personal_finance_category_version"] != "v2" {
				t.Errorf("sync options = %#v", options)
			}
			return jsonHTTPResponse(http.StatusOK, `{
				"transactions_update_status":"HISTORICAL_UPDATE_COMPLETE",
				"accounts":[],
				"added":[{
					"account_id":"checking-1",
					"amount":1.005,
					"iso_currency_code":"USD",
					"unofficial_currency_code":null,
					"date":"2026-07-01",
					"name":"RAW NAME",
					"original_description":"Original Name",
					"pending":true,
					"pending_transaction_id":null,
					"transaction_id":"transaction-1",
					"personal_finance_category":{"primary":"FOOD_AND_DRINK","detailed":"FOOD_AND_DRINK_COFFEE"}
				}],
				"modified":[],
				"removed":[{"transaction_id":"removed-1"}],
				"next_cursor":"cursor-2",
				"has_more":false,
				"request_id":"request-1"
			}`), nil
		case "/accounts/get":
			return jsonHTTPResponse(http.StatusOK, `{
				"accounts":[{
					"account_id":"checking-1",
					"balances":{"available":90.01,"current":100.01,"limit":null,"iso_currency_code":"USD","unofficial_currency_code":null},
					"mask":"0000",
					"name":"Test Checking",
					"official_name":null,
					"type":"depository",
					"subtype":"checking"
				}],
				"item":{"item_id":"item-1","institution_id":"ins_test","institution_name":"Sandbox Bank","webhook":null,"error":null,"available_products":[],"billed_products":[],"consent_expiration_time":null,"update_type":"background"},
				"request_id":"request-2"
			}`), nil
		case "/liabilities/get":
			return jsonHTTPResponse(http.StatusOK, `{
				"accounts":[{
					"account_id":"card-1",
					"balances":{"available":900.00,"current":100.00,"limit":1000.00,"iso_currency_code":"USD","unofficial_currency_code":null},
					"mask":"1111",
					"name":"Test Card",
					"official_name":null,
					"type":"credit",
					"subtype":"credit card"
				}],
				"item":{"item_id":"item-1","webhook":null,"error":null,"available_products":[],"billed_products":[],"consent_expiration_time":null,"update_type":"background"},
				"liabilities":{"credit":[{
					"account_id":"card-1",
					"aprs":[{"apr_percentage":19.99,"apr_type":"purchase_apr","balance_subject_to_apr":null,"interest_charge_amount":null}],
					"is_overdue":false,
					"last_payment_amount":null,
					"last_payment_date":null,
					"last_statement_issue_date":"2026-07-05",
					"last_statement_balance":4.35,
					"minimum_payment_amount":1.005,
					"next_payment_due_date":"2026-07-28"
				}],"mortgage":[],"student":[]},
				"request_id":"request-3"
			}`), nil
		case "/item/get":
			return jsonHTTPResponse(http.StatusOK, `{
				"item":{"item_id":"item-1","institution_id":"ins_test","institution_name":"Sandbox Bank","webhook":null,"error":{"error_type":"ITEM_ERROR","error_code":"ITEM_LOGIN_REQUIRED","error_message":"fake","display_message":null},"available_products":[],"billed_products":[],"consent_expiration_time":null,"update_type":"background"},
				"status":null,
				"request_id":"request-4"
			}`), nil
		default:
			t.Fatalf("unexpected Plaid request path %q", request.URL.Path)
			return nil, nil
		}
	})}

	config, err := NewConfig(fakeClientID, fakeSecret, EnvironmentSandbox)
	if err != nil {
		t.Fatalf("NewConfig() error: %v", err)
	}
	client, err := newSDKClient(config, httpClient)
	if err != nil {
		t.Fatalf("newSDKClient() error: %v", err)
	}
	gateway := &sdkGateway{client: client}

	page, err := gateway.transactionsSync(context.Background(), fakeAccessToken, "cursor-1")
	if err != nil {
		t.Fatalf("transactionsSync() error: %v", err)
	}
	if page.NextCursor != "cursor-2" || len(page.Added) != 1 || page.Added[0].Amount != 1.005 {
		t.Errorf("parsed sync page = %#v", page)
	}
	if page.Added[0].OriginalDescription != "Original Name" ||
		page.Added[0].Category != "FOOD_AND_DRINK" || !page.Added[0].Pending {
		t.Errorf("parsed transaction = %#v", page.Added[0])
	}
	if !reflect.DeepEqual(page.Removed, []string{"removed-1"}) {
		t.Errorf("parsed removals = %v", page.Removed)
	}

	accounts, err := gateway.accounts(context.Background(), fakeAccessToken)
	if err != nil {
		t.Fatalf("accounts() error: %v", err)
	}
	if len(accounts) != 1 || accounts[0].Current == nil || *accounts[0].Current != 100.01 {
		t.Errorf("parsed accounts = %#v", accounts)
	}

	liabilities, err := gateway.liabilities(context.Background(), fakeAccessToken)
	if err != nil {
		t.Fatalf("liabilities() error: %v", err)
	}
	if len(liabilities.Accounts) != 1 || len(liabilities.Credit) != 1 {
		t.Fatalf("parsed liabilities = %#v", liabilities)
	}
	if liabilities.Credit[0].MinimumPayment == nil ||
		*liabilities.Credit[0].MinimumPayment != 1.005 ||
		liabilities.Credit[0].APRs[0].Percentage != 19.99 {
		t.Errorf("parsed credit liability = %#v", liabilities.Credit[0])
	}

	item, err := gateway.item(context.Background(), fakeAccessToken)
	if err != nil {
		t.Fatalf("item() error: %v", err)
	}
	if item.ID != "item-1" || item.Institution != "Sandbox Bank" ||
		item.ErrorCode != errorItemLoginRequired {
		t.Errorf("parsed item = %#v", item)
	}

	wantPaths := []string{"/transactions/sync", "/accounts/get", "/liabilities/get", "/item/get"}
	if !reflect.DeepEqual(requestedPaths, wantPaths) {
		t.Errorf("request paths = %v, want %v", requestedPaths, wantPaths)
	}
}

func TestSDKGatewaySanitizesPlaidErrorBody(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonHTTPResponse(http.StatusBadRequest, `{
			"error_type":"ITEM_ERROR",
			"error_code":"ITEM_LOGIN_REQUIRED",
			"error_message":"token `+fakeAccessToken+` must be refreshed",
			"display_message":null,
			"request_id":"request-error"
		}`), nil
	})}
	config, err := NewConfig(fakeClientID, fakeSecret, EnvironmentSandbox)
	if err != nil {
		t.Fatalf("NewConfig() error: %v", err)
	}
	client, err := newSDKClient(config, httpClient)
	if err != nil {
		t.Fatalf("newSDKClient() error: %v", err)
	}

	_, err = (&sdkGateway{client: client}).transactionsSync(
		context.Background(),
		fakeAccessToken,
		"",
	)
	if errorCode(err) != errorItemLoginRequired {
		t.Fatalf("transactionsSync() error = %v, want ITEM_LOGIN_REQUIRED", err)
	}
	if strings.Contains(err.Error(), fakeAccessToken) || strings.Contains(err.Error(), "must be refreshed") {
		t.Fatalf("sanitized error exposes provider body: %q", err)
	}
}

func TestSDKGatewayBuildsLinkTokenAndExchangeRequests(t *testing.T) {
	var requestedPaths []string
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestedPaths = append(requestedPaths, request.URL.Path)
		if request.Header.Get("PLAID-CLIENT-ID") != fakeClientID ||
			request.Header.Get("PLAID-SECRET") != fakeSecret {
			t.Error("request is missing Plaid credential headers")
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		var fields map[string]any
		if err := json.Unmarshal(body, &fields); err != nil {
			t.Fatalf("decode request body: %v", err)
		}

		switch request.URL.Path {
		case "/link/token/create":
			if fields["client_name"] != "Moneta" || fields["language"] != "en" {
				t.Errorf("Link client/language = %v/%v", fields["client_name"], fields["language"])
			}
			if !reflect.DeepEqual(fields["country_codes"], []any{"US"}) ||
				!reflect.DeepEqual(fields["products"], []any{"transactions"}) ||
				!reflect.DeepEqual(fields["required_if_supported_products"], []any{"liabilities"}) {
				t.Errorf("Link products/countries = %#v", fields)
			}
			user, ok := fields["user"].(map[string]any)
			if !ok || user["client_user_id"] != defaultClientUserID {
				t.Errorf("Link user = %#v", fields["user"])
			}
			transactions, ok := fields["transactions"].(map[string]any)
			if !ok || transactions["days_requested"] != float64(maxTransactionHistoryDays) {
				t.Errorf("Link transaction options = %#v", fields["transactions"])
			}
			return jsonHTTPResponse(http.StatusOK, `{
				"link_token":"`+fakeLinkToken+`",
				"expiration":"2026-07-17T12:00:00Z",
				"request_id":"request-link"
			}`), nil
		case "/item/public_token/exchange":
			if fields["public_token"] != fakePublicToken {
				t.Errorf("exchange public token = %v", fields["public_token"])
			}
			return jsonHTTPResponse(http.StatusOK, `{
				"access_token":"`+fakePermanentToken+`",
				"item_id":"item-fake",
				"request_id":"request-exchange"
			}`), nil
		default:
			t.Fatalf("unexpected Plaid request path %q", request.URL.Path)
			return nil, nil
		}
	})}

	config, err := NewConfig(fakeClientID, fakeSecret, EnvironmentSandbox)
	if err != nil {
		t.Fatalf("NewConfig() error: %v", err)
	}
	client, err := newSDKClient(config, httpClient)
	if err != nil {
		t.Fatalf("newSDKClient() error: %v", err)
	}
	gateway := &sdkGateway{client: client}

	linkToken, err := gateway.createLinkToken(context.Background(), defaultClientUserID)
	if err != nil {
		t.Fatalf("createLinkToken() error: %v", err)
	}
	if linkToken != fakeLinkToken {
		t.Errorf("link token = %q", linkToken)
	}
	exchange, err := gateway.exchangePublicToken(context.Background(), fakePublicToken)
	if err != nil {
		t.Fatalf("exchangePublicToken() error: %v", err)
	}
	if exchange.ItemID != "item-fake" || exchange.AccessToken != fakePermanentToken {
		t.Errorf("exchange result = %#v", exchange)
	}
	if !reflect.DeepEqual(
		requestedPaths,
		[]string{"/link/token/create", "/item/public_token/exchange"},
	) {
		t.Errorf("request paths = %v", requestedPaths)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func jsonHTTPResponse(status int, body string) *http.Response {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     header,
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}
}
