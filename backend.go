package orders

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type Backend interface {
	Event(context.Context, string) (Event, error)
	Events(context.Context, string) ([]Event, error)
	Reserve(context.Context, Order) (Quote, error)
	Release(context.Context, Order) error
	Confirm(context.Context, Order) error
	Charge(context.Context, Order, Card) (Payment, error)
	Payment(context.Context, string) (Payment, error)
	Issue(context.Context, IssueInput) ([]Ticket, error)
	Notify(context.Context, Order, User, []Ticket) bool
}
type HTTPBackend struct {
	config Config
	client *http.Client
}

func NewHTTPBackend(config Config) *HTTPBackend {
	return &HTTPBackend{config: config, client: &http.Client{Timeout: config.HTTPTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func endpoint(origin string, segments ...string) string {
	return strings.TrimRight(origin, "/") + "/" + strings.Join(segments, "/")
}
func (b *HTTPBackend) request(ctx context.Context, method, address string, input, output any, allowed ...int) error {
	var data []byte
	var err error
	if input != nil {
		data, err = json.Marshal(input)
		if err != nil {
			return &UpstreamError{Status: 503}
		}
	}
	request, err := http.NewRequestWithContext(ctx, method, address, bytes.NewReader(data))
	if err != nil {
		return &UpstreamError{Status: 503}
	}
	request.Header.Set("X-Internal-Key", b.config.InternalAPIKey)
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := b.client.Do(request)
	if err != nil {
		return &UpstreamError{Status: 503}
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 32*1024*1024+1))
	if err != nil || len(raw) > 32*1024*1024 {
		return &UpstreamError{Status: 503}
	}
	accepted := response.StatusCode == 200
	for _, status := range allowed {
		accepted = accepted || response.StatusCode == status
	}
	if !accepted {
		var envelope struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &envelope)
		return &UpstreamError{Status: response.StatusCode, Code: envelope.Error.Code}
	}
	if output != nil && json.Unmarshal(raw, output) != nil {
		return &UpstreamError{Status: 503}
	}
	return nil
}
func (b *HTTPBackend) Event(ctx context.Context, id string) (Event, error) {
	var event Event
	err := b.request(ctx, "GET", endpoint(b.config.EventsURL, "internal", "events", url.PathEscape(id)), nil, &event)
	return event, err
}
func (b *HTTPBackend) Events(ctx context.Context, owner string) ([]Event, error) {
	var events []Event
	err := b.request(ctx, "GET", endpoint(b.config.EventsURL, "internal", "events")+"?organizerId="+url.QueryEscape(owner), nil, &events)
	if err == nil && events == nil {
		err = &UpstreamError{Status: 503}
	}
	return events, err
}
func reservationURL(origin string, o Order, action string) string {
	return endpoint(origin, "internal", "events", url.PathEscape(o.EventID), "sectors", url.PathEscape(o.SectorID), action)
}
func (b *HTTPBackend) Reserve(ctx context.Context, o Order) (Quote, error) {
	var quote Quote
	err := b.request(ctx, "POST", reservationURL(b.config.EventsURL, o, "reserve"), map[string]any{"orderId": o.ID, "quantity": o.Quantity}, &quote)
	return quote, err
}
func (b *HTTPBackend) reservationAction(ctx context.Context, o Order, action string) error {
	var result struct {
		OrderID           string `json:"orderId"`
		ReservationStatus string `json:"reservationStatus"`
		Confirmed         bool   `json:"confirmed"`
		Released          bool   `json:"released"`
	}
	if err := b.request(ctx, "POST", reservationURL(b.config.EventsURL, o, action), map[string]string{"orderId": o.ID}, &result); err != nil {
		return err
	}
	if result.OrderID != o.ID || (action == "confirm" && (!result.Confirmed || result.Released || result.ReservationStatus != "confirmed")) || (action == "release" && (!result.Released || result.Confirmed || result.ReservationStatus != "released")) {
		return &UpstreamError{Status: 503}
	}
	return nil
}
func (b *HTTPBackend) Release(ctx context.Context, o Order) error {
	return b.reservationAction(ctx, o, "release")
}
func (b *HTTPBackend) Confirm(ctx context.Context, o Order) error {
	return b.reservationAction(ctx, o, "confirm")
}
func (b *HTTPBackend) Charge(ctx context.Context, o Order, card Card) (Payment, error) {
	var payment Payment
	err := b.request(ctx, "POST", endpoint(b.config.PaymentsURL, "internal", "payments", "charge"), map[string]any{"orderId": o.ID, "amount": o.Total, "currency": "BOB", "card": card}, &payment)
	return payment, err
}
func (b *HTTPBackend) Payment(ctx context.Context, id string) (Payment, error) {
	var payment Payment
	err := b.request(ctx, "GET", endpoint(b.config.PaymentsURL, "internal", "payments", "by-order", url.PathEscape(id)), nil, &payment)
	return payment, err
}
func (b *HTTPBackend) Issue(ctx context.Context, input IssueInput) ([]Ticket, error) {
	var result struct {
		Tickets []Ticket `json:"tickets"`
	}
	err := b.request(ctx, "POST", endpoint(b.config.TicketsURL, "internal", "tickets", "issue"), input, &result, 201)
	return result.Tickets, err
}
func (b *HTTPBackend) Notify(ctx context.Context, o Order, user User, tickets []Ticket) bool {
	if b.config.NotificationsURL == "" {
		return false
	}
	var response struct {
		Sent bool `json:"sent"`
	}
	refs := make([]map[string]string, 0, len(tickets))
	for _, ticket := range tickets {
		refs = append(refs, map[string]string{"id": ticket.ID, "code": ticket.Code})
	}
	payload := map[string]any{"to": user.Email, "customerName": user.Name, "orderId": o.ID, "eventName": o.EventName, "eventDate": o.EventDate, "venue": o.Venue, "city": o.City, "sectorName": o.SectorName, "quantity": o.Quantity, "total": o.Total, "tickets": refs}
	return b.request(ctx, "POST", endpoint(b.config.NotificationsURL, "internal", "notifications", "order-confirmation"), payload, &response) == nil && response.Sent
}
