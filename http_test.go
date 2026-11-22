package orders

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/golang-jwt/jwt/v5"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func jwtToken(t *testing.T, id, role string, expiry time.Time, algorithm jwt.SigningMethod, secret string) string {
	t.Helper()
	token, err := jwt.NewWithClaims(algorithm, jwt.MapClaims{"sub": id, "name": "Demo", "email": "demo@example.com", "role": role, "iat": time.Now().Add(-time.Minute).Unix(), "exp": expiry.Unix()}).SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	return token
}
func jwtFor(t *testing.T, id, role string) string {
	return jwtToken(t, id, role, time.Now().Add(time.Hour), jwt.SigningMethodHS256, testSecret)
}
func httpCall(t *testing.T, s *Service, method, path, token, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	out := httptest.NewRecorder()
	s.Handler(nil).ServeHTTP(out, req)
	return out
}
func decodeOrder(t *testing.T, response *httptest.ResponseRecorder) Order {
	t.Helper()
	var order Order
	if err := json.Unmarshal(response.Body.Bytes(), &order); err != nil {
		t.Fatal(err)
	}
	return order
}
func inputJSON(t *testing.T) string {
	t.Helper()
	raw, err := json.Marshal(createInput())
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
func errorCode(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var value struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(response.Body.Bytes(), &value) != nil || value.Error.Message == "" {
		t.Fatal(response.Body.String())
	}
	return value.Error.Code
}
func TestHTTPCreateReplayHistoryAndStats(t *testing.T) {
	f := fake()
	s := newService(t, f, nil)
	customerToken := jwtFor(t, "u-cli-1", "customer")
	response := httpCall(t, s, "POST", "/orders", customerToken, "key", inputJSON(t))
	if response.Code != 201 {
		t.Fatal(response.Code, response.Body.String())
	}
	order := decodeOrder(t, response)
	if order.UserID != "u-cli-1" || order.Status != "paid" || order.TicketIDs == nil || order.RejectionReason != nil {
		t.Fatal(order)
	}
	response = httpCall(t, s, "POST", "/orders", customerToken, "key", inputJSON(t))
	if response.Code != 200 || decodeOrder(t, response).ID != order.ID {
		t.Fatal(response.Body.String())
	}
	for _, path := range []string{"/orders/mine", "/orders/" + order.ID} {
		if response = httpCall(t, s, "GET", path, customerToken, "", ""); response.Code != 200 {
			t.Fatal(response.Code, response.Body.String())
		}
	}
	response = httpCall(t, s, "GET", "/orders/"+order.ID, jwtFor(t, "u-cli-2", "customer"), "", "")
	if response.Code != 404 {
		t.Fatal(response.Code)
	}
	response = httpCall(t, s, "GET", "/orders/stats?eventId=e-demo", jwtFor(t, "u-org-1", "organizer"), "", "")
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"ticketsSold":2`) {
		t.Fatal(response.Code, response.Body.String())
	}
}
func TestHTTPValidationAndRoleErrors(t *testing.T) {
	s := newService(t, fake(), nil)
	customerToken := jwtFor(t, "u-cli-1", "customer")
	for _, item := range []struct {
		token, key, body string
		status           int
		code             string
	}{{"", "key", inputJSON(t), 401, "unauthorized"}, {jwtFor(t, "u-org-1", "organizer"), "key", inputJSON(t), 403, "forbidden"}, {customerToken, "", inputJSON(t), 400, "invalid_request"}, {customerToken, "key", "{", 400, "invalid_request"}, {customerToken, "key", strings.Replace(inputJSON(t), `"quantity":2`, `"quantity":7`, 1), 400, "invalid_quantity"}, {customerToken, "key", strings.Replace(inputJSON(t), `"quantity":2`, `"quantity":1.5`, 1), 400, "invalid_quantity"}, {customerToken, "key", strings.Replace(inputJSON(t), `"expiry":"08/28"`, `"expiry":"13/28"`, 1), 400, "invalid_request"}} {
		response := httpCall(t, s, "POST", "/orders", item.token, item.key, item.body)
		if response.Code != item.status || errorCode(t, response) != item.code {
			t.Fatal(response.Code, response.Body.String(), item)
		}
	}
	if len(s.Mine("u-cli-1")) != 0 {
		t.Fatal("invalid requests created orders")
	}
}
func TestHTTPJWTRejectsExpiredWrongAlgorithmSignatureAndClaims(t *testing.T) {
	s := newService(t, fake(), nil)
	tokens := []string{"bad.jwt.token", jwtToken(t, "u-cli-1", "customer", time.Now().Add(-time.Second), jwt.SigningMethodHS256, testSecret), jwtToken(t, "u-cli-1", "customer", time.Now().Add(time.Hour), jwt.SigningMethodHS512, testSecret), jwtToken(t, "u-cli-1", "customer", time.Now().Add(time.Hour), jwt.SigningMethodHS256, "another-secret-with-at-least-32-characters"), jwtFor(t, "", "customer"), jwtFor(t, "u-cli-1", "admin")}
	for _, token := range tokens {
		response := httpCall(t, s, "GET", "/orders/mine", token, "", "")
		if response.Code != 401 || errorCode(t, response) != "unauthorized" {
			t.Fatal(response.Code, response.Body.String())
		}
	}
}
func TestHTTPPendingHasLocationAndCompleteEnvelope(t *testing.T) {
	f := fake()
	f.failCharge = 1
	s := newService(t, f, nil)
	response := httpCall(t, s, "POST", "/orders", jwtFor(t, "u-cli-1", "customer"), "pending", inputJSON(t))
	order := decodeOrder(t, response)
	if response.Code != 202 || response.Header().Get("Location") != "/api/orders/"+order.ID || order.PaymentStatus != "unknown" || order.TicketIDs == nil {
		t.Fatal(response.Code, order, response.Header())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("private response cacheable")
	}
}
func TestHTTPDuplicateHeadersUnknownAndMethod(t *testing.T) {
	s := newService(t, fake(), nil)
	req := httptest.NewRequest("POST", "/orders", strings.NewReader(inputJSON(t)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+jwtFor(t, "u-cli-1", "customer"))
	req.Header.Add("Idempotency-Key", "same")
	req.Header.Add("Idempotency-Key", "same")
	out := httptest.NewRecorder()
	s.Handler(nil).ServeHTTP(out, req)
	if out.Code != 400 {
		t.Fatal(out.Code)
	}
	out = httpCall(t, s, "DELETE", "/orders", "", "", "")
	if out.Code != 405 || out.Header().Get("Allow") != "POST" {
		t.Fatal(out.Code)
	}
	out = httpCall(t, s, "GET", "/unknown", "", "", "")
	if out.Code != 404 {
		t.Fatal(out.Code)
	}
	out = httpCall(t, s, "GET", "/health", "", "", "")
	if out.Code != 200 || !strings.Contains(out.Body.String(), `"storage":"memory"`) {
		t.Fatal(out.Code)
	}
}
func TestLogsAndStoredOrderNeverContainCardOrToken(t *testing.T) {
	s := newService(t, fake(), nil)
	token := jwtFor(t, "u-cli-1", "customer")
	req := httptest.NewRequest("POST", "/orders", strings.NewReader(inputJSON(t)))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", "private")
	req.Header.Set("Content-Type", "application/json")
	var logs bytes.Buffer
	response := httptest.NewRecorder()
	s.Handler(slog.New(slog.NewJSONHandler(&logs, nil))).ServeHTTP(response, req)
	stored, _ := json.Marshal(s.Mine("u-cli-1"))
	for _, sensitive := range []string{createInput().Card.Number, token, `"cvc"`, `"number"`} {
		if strings.Contains(logs.String(), sensitive) || strings.Contains(string(stored), sensitive) || strings.Contains(response.Body.String(), sensitive) {
			t.Fatal("sensitive data retained/exposed")
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestHTTPBackendUsesInternalRoutesAndSchemasWithoutPublicToken(t *testing.T) {
	config := testConfig()
	config.NotificationsURL = "http://localhost:4006"
	backend := NewHTTPBackend(config)
	backend.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("X-Internal-Key") != testKey || r.Header.Get("Authorization") != "" {
			t.Fatal("wrong credentials")
		}
		response := `{}`
		switch {
		case r.URL.Path == "/internal/events/e-demo":
			response = `{"id":"e-demo","organizerId":"u-org-1","name":"Demo","date":"2030-01-03T00:00:00Z","venue":"Teatro","city":"La Paz","status":"published","sectors":[]}`
		case r.URL.Path == "/internal/payments/by-order/o-1":
			response = `{"orderId":"o-1","status":"approved"}`
		case r.URL.Path == "/internal/notifications/order-confirmation":
			response = `{"sent":true}`
		default:
			t.Fatal("unexpected route", r.URL.String())
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(response)), Header: make(http.Header)}, nil
	})
	event, err := backend.Event(context.Background(), "e-demo")
	if err != nil || event.ID != "e-demo" {
		t.Fatal(event, err)
	}
	payment, err := backend.Payment(context.Background(), "o-1")
	if err != nil || payment.OrderID != "o-1" {
		t.Fatal(payment, err)
	}
	if !backend.Notify(context.Background(), Order{}, customer(), nil) {
		t.Fatal("expected accepted sent=true 200")
	}
	backend.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 202, Body: io.NopCloser(strings.NewReader(`{"sent":true}`)), Header: make(http.Header)}, nil
	})
	if backend.Notify(context.Background(), Order{}, customer(), nil) {
		t.Fatal("202 is not sent proof")
	}
}
func TestPreflightClosedOrUnknownEventDoesNotCreateOrder(t *testing.T) {
	for _, status := range []string{"draft", "cancelled", "unknown"} {
		f := fake()
		f.event.Status = status
		s := newService(t, f, nil)
		_, _, err := s.Create(context.Background(), customer(), "preflight", createInput())
		if err == nil || err.Status != 404 || len(s.Mine(customer().ID)) != 0 || f.reserveCalls != 0 {
			t.Fatal(err)
		}
	}
}

func TestPurchaseUsingProductionHTTPBackendWireContracts(t *testing.T) {
	config := testConfig()
	backend := NewHTTPBackend(config)
	sequence := []string{}
	var orderID string
	backend.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("X-Internal-Key") != testKey {
			return nil, errors.New("missing internal key")
		}
		status := 200
		var output any
		switch {
		case r.Method == "GET" && r.URL.Path == "/internal/events/e-demo":
			output = testEvent()
			sequence = append(sequence, "event")
		case strings.HasSuffix(r.URL.Path, "/reserve"):
			var in struct {
				OrderID  string `json:"orderId"`
				Quantity int    `json:"quantity"`
			}
			if json.NewDecoder(r.Body).Decode(&in) != nil || in.Quantity != 2 {
				return nil, errors.New("invalid reservation payload")
			}
			orderID = in.OrderID
			output = Quote{Reserved: true, OrderID: orderID, EventID: "e-demo", SectorID: "s-general", Quantity: 2, UnitPrice: 9000, Total: 18000, Currency: "BOB", ReservationStatus: "reserved", EventName: "Nombre congelado", EventDate: testClock().Add(48 * time.Hour), Venue: "Lugar congelado", City: "Sucre", SectorName: "Sector congelado"}
			sequence = append(sequence, "reserve")
		case r.URL.Path == "/internal/payments/charge":
			var in struct {
				OrderID  string `json:"orderId"`
				Amount   Money  `json:"amount"`
				Currency string `json:"currency"`
				Card     Card   `json:"card"`
			}
			if json.NewDecoder(r.Body).Decode(&in) != nil || in.OrderID != orderID || in.Amount != 18000 || in.Currency != "BOB" || in.Card.Number != createInput().Card.Number {
				return nil, errors.New("invalid charge payload")
			}
			output = Payment{TransactionID: "pay-1", OrderID: orderID, Status: "approved", Amount: 18000, Currency: "BOB", CardLast4: "4242", CreatedAt: testClock()}
			sequence = append(sequence, "charge")
		case strings.HasSuffix(r.URL.Path, "/confirm"):
			output = map[string]any{"orderId": orderID, "reservationStatus": "confirmed", "confirmed": true, "released": false}
			sequence = append(sequence, "confirm")
		case r.URL.Path == "/internal/tickets/issue":
			var input IssueInput
			if json.NewDecoder(r.Body).Decode(&input) != nil || input.OrderID != orderID || input.UserID != "u-cli-1" || input.EventName != "Nombre congelado" || input.Venue != "Lugar congelado" || input.SectorName != "Sector congelado" || input.Quantity != 2 {
				return nil, errors.New("invalid issue snapshot")
			}
			status = 201
			output = map[string]any{"tickets": []Ticket{{ID: "t-1", Code: "EY-AAAA-AAAA", OrderID: orderID, UserID: input.UserID, EventID: input.EventID, Status: "valid"}, {ID: "t-2", Code: "EY-BBBB-BBBB", OrderID: orderID, UserID: input.UserID, EventID: input.EventID, Status: "valid"}}}
			sequence = append(sequence, "issue")
		default:
			return nil, errors.New("unexpected HTTP route")
		}
		raw, err := json.Marshal(output)
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(raw))}, nil
	})
	s, err := New(config, backend, testClock)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	order, status, apiErr := s.Create(context.Background(), customer(), "wire-contract", createInput())
	if apiErr != nil || status != 201 || order.Status != "paid" || order.Total != 18000 || order.EventName != "Nombre congelado" || len(order.TicketIDs) != 2 || order.EmailSent {
		t.Fatal(order, status, apiErr)
	}
	if strings.Join(sequence, ",") != "event,reserve,charge,confirm,issue" {
		t.Fatal(sequence)
	}
}
