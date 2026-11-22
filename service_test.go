package orders

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

const testSecret = "test-only-jwt-secret-with-at-least-32-bytes"
const testKey = "test-only-internal-key-with-at-least-32-bytes"

func testClock() time.Time { return time.Date(2030, 1, 2, 2, 30, 0, 0, time.UTC) }
func testConfig() Config {
	return Config{Port: 4003, JWTSecret: testSecret, InternalAPIKey: testKey, EventsURL: "http://localhost:4002", PaymentsURL: "http://localhost:4004", TicketsURL: "http://localhost:4005", HTTPTimeout: time.Second, OperationTimeout: time.Second, RecoveryInterval: time.Hour}
}
func customer() User {
	return User{ID: "u-cli-1", Name: "Camila Rojas", Email: "cliente@entradasya.bo", Role: "customer"}
}
func createInput() CreateInput {
	return CreateInput{EventID: "e-demo", SectorID: "s-general", Quantity: 2, Card: Card{Number: "4242424242424242", Holder: "Camila Rojas", Expiry: "08/28", CVC: "123"}}
}
func testEvent() Event {
	return Event{ID: "e-demo", OrganizerID: "u-org-1", Name: "Evento Demo", Date: testClock().Add(24 * time.Hour), Venue: "Teatro", City: "La Paz", Status: "published", Sectors: []Sector{{ID: "s-general", Name: "General", Price: 8500, Capacity: 100, Available: 100}}}
}

type fakeBackend struct {
	mu                                                                sync.Mutex
	event                                                             Event
	payments                                                          map[string]Payment
	quotes                                                            map[string]Quote
	tickets                                                           map[string][]Ticket
	sequence                                                          []string
	reserveCalls, chargeCalls, confirmCalls, issueCalls, releaseCalls int
	failReserve, failCharge, failConfirm, failIssue, failRelease      int
	reserveApplied, chargeApplied, issueApplied                       bool
	notify                                                            bool
	delayReserve                                                      time.Duration
}

func fake() *fakeBackend {
	return &fakeBackend{event: testEvent(), payments: map[string]Payment{}, quotes: map[string]Quote{}, tickets: map[string][]Ticket{}}
}
func (f *fakeBackend) Event(_ context.Context, id string) (Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sequence = append(f.sequence, "event")
	if f.event.ID != id {
		return Event{}, &UpstreamError{Status: 404}
	}
	e := f.event
	e.Sectors = append([]Sector{}, e.Sectors...)
	return e, nil
}
func (f *fakeBackend) Events(_ context.Context, owner string) ([]Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.event.OrganizerID != owner {
		return []Event{}, nil
	}
	e := f.event
	e.Sectors = append([]Sector{}, e.Sectors...)
	return []Event{e}, nil
}
func (f *fakeBackend) Reserve(ctx context.Context, o Order) (Quote, error) {
	f.mu.Lock()
	delay := f.delayReserve
	f.mu.Unlock()
	if delay > 0 {
		select {
		case <-ctx.Done():
			return Quote{}, ctx.Err()
		case <-time.After(delay):
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reserveCalls++
	f.sequence = append(f.sequence, "reserve")
	failing := f.failReserve > 0
	if failing {
		f.failReserve--
		if !f.reserveApplied {
			return Quote{}, errors.New("network unavailable")
		}
	}
	quote, exists := f.quotes[o.ID]
	if !exists {
		sector := &f.event.Sectors[0]
		if sector.Available < uint32(o.Quantity) {
			return Quote{}, &UpstreamError{Status: 409, Code: "sold_out"}
		}
		sector.Available -= uint32(o.Quantity)
		quote = Quote{Reserved: true, OrderID: o.ID, EventID: o.EventID, SectorID: o.SectorID, Quantity: o.Quantity, UnitPrice: sector.Price, Total: sector.Price * Money(o.Quantity), Currency: "BOB", ReservationStatus: "reserved", EventName: f.event.Name, EventDate: f.event.Date, Venue: f.event.Venue, City: f.event.City, SectorName: sector.Name}
		f.quotes[o.ID] = quote
	}
	if failing {
		return Quote{}, errors.New("response lost")
	}
	return quote, nil
}
func (f *fakeBackend) Release(_ context.Context, o Order) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releaseCalls++
	f.sequence = append(f.sequence, "release")
	if f.failRelease > 0 {
		f.failRelease--
		return errors.New("release unavailable")
	}
	q := f.quotes[o.ID]
	if q.ReservationStatus != "released" {
		f.event.Sectors[0].Available += uint32(q.Quantity)
		q.ReservationStatus = "released"
		f.quotes[o.ID] = q
	}
	return nil
}
func (f *fakeBackend) Confirm(_ context.Context, o Order) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.confirmCalls++
	f.sequence = append(f.sequence, "confirm")
	if f.failConfirm > 0 {
		f.failConfirm--
		return errors.New("confirm unavailable")
	}
	q := f.quotes[o.ID]
	q.ReservationStatus = "confirmed"
	f.quotes[o.ID] = q
	return nil
}
func (f *fakeBackend) Charge(_ context.Context, o Order, card Card) (Payment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chargeCalls++
	f.sequence = append(f.sequence, "charge")
	failing := f.failCharge > 0
	if failing {
		f.failCharge--
		if !f.chargeApplied {
			return Payment{}, errors.New("network unavailable")
		}
	}
	p, ok := f.payments[o.ID]
	if !ok {
		status := "approved"
		if card.Number[len(card.Number)-4:] == "0000" {
			status = "declined"
		}
		p = Payment{TransactionID: "pay-" + o.ID, OrderID: o.ID, Status: status, Amount: o.Total, Currency: "BOB", CardLast4: card.Number[len(card.Number)-4:], CreatedAt: testClock()}
		f.payments[o.ID] = p
	}
	if failing {
		return Payment{}, errors.New("response lost")
	}
	return p, nil
}
func (f *fakeBackend) Payment(_ context.Context, id string) (Payment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sequence = append(f.sequence, "lookup")
	p, ok := f.payments[id]
	if !ok {
		return p, &UpstreamError{Status: 404}
	}
	return p, nil
}
func (f *fakeBackend) Issue(_ context.Context, input IssueInput) ([]Ticket, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.issueCalls++
	f.sequence = append(f.sequence, "issue")
	failing := f.failIssue > 0
	if failing {
		f.failIssue--
		if !f.issueApplied {
			return nil, errors.New("tickets unavailable")
		}
	}
	tickets, ok := f.tickets[input.OrderID]
	if !ok {
		for i := 0; i < input.Quantity; i++ {
			tickets = append(tickets, Ticket{ID: fmt.Sprintf("ticket-%s-%d", input.OrderID, i), Code: fmt.Sprintf("CODE-%s-%d", input.OrderID, i), OrderID: input.OrderID, UserID: input.UserID, EventID: input.EventID, Status: "valid"})
		}
		f.tickets[input.OrderID] = tickets
	}
	if failing {
		return nil, errors.New("response lost")
	}
	return append([]Ticket{}, tickets...), nil
}
func (f *fakeBackend) Notify(_ context.Context, _ Order, _ User, _ []Ticket) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sequence = append(f.sequence, "notify")
	return f.notify
}
func newService(t *testing.T, backend Backend, change func(*Config)) *Service {
	t.Helper()
	config := testConfig()
	if change != nil {
		change(&config)
	}
	s, err := New(config, backend, testClock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}
func create(t *testing.T, s *Service, key string, input CreateInput) (Order, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	order, status, err := s.Create(ctx, customer(), key, input)
	if err != nil {
		t.Fatal(err.Code)
	}
	return order, status
}
func awaitCompleted(t *testing.T, s *Service, id string) Order {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		order, err := s.Get(id, customer().ID)
		if err != nil {
			t.Fatal(err)
		}
		if order.ProcessingStatus == "completed" {
			return order
		}
		time.Sleep(time.Millisecond)
	}
	order, _ := s.Get(id, customer().ID)
	t.Fatalf("order did not finish: %+v", order)
	return Order{}
}
func TestCompletePurchaseAndReplay(t *testing.T) {
	f := fake()
	f.notify = true
	s := newService(t, f, nil)
	order, status := create(t, s, "key", createInput())
	if status != 201 || order.Status != "paid" || order.ProcessingStatus != "completed" || order.PaymentStatus != "approved" || order.TicketIssuanceStatus != "issued" || len(order.TicketIDs) != 2 || !order.EmailSent {
		t.Fatal(order, status)
	}
	again, status := create(t, s, "key", createInput())
	if status != 200 || again.ID != order.ID || f.chargeCalls != 1 || f.reserveCalls != 1 || f.issueCalls != 1 {
		t.Fatal(again, status)
	}
	want := []string{"event", "reserve", "charge", "confirm", "issue", "notify"}
	if fmt.Sprint(f.sequence) != fmt.Sprint(want) {
		t.Fatal(f.sequence)
	}
}
func TestDeclinedPaymentReleasesCapacity(t *testing.T) {
	f := fake()
	s := newService(t, f, nil)
	input := createInput()
	input.Card.Number = "4000000000000000"
	order, status := create(t, s, "declined", input)
	if status != 201 || order.Status != "rejected" || *order.RejectionReason != "payment_declined" || order.PaymentStatus != "declined" || order.ProcessingStatus != "completed" || f.event.Sectors[0].Available != 100 || f.issueCalls != 0 {
		t.Fatal(order, status)
	}
}
func TestSoldOutNeverCallsPayments(t *testing.T) {
	f := fake()
	f.event.Sectors[0].Available = 0
	s := newService(t, f, nil)
	order, status := create(t, s, "sold", createInput())
	if status != 201 || order.Status != "rejected" || *order.RejectionReason != "sold_out" || f.chargeCalls != 0 {
		t.Fatal(order, status)
	}
}
func TestConcurrentIdempotencyAndUserScope(t *testing.T) {
	f := fake()
	s := newService(t, f, nil)
	var wg sync.WaitGroup
	ids := make(chan string, 30)
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o, _, err := s.Create(context.Background(), customer(), "same", createInput())
			if err != nil {
				t.Error(err)
				return
			}
			ids <- o.ID
		}()
	}
	wg.Wait()
	close(ids)
	seen := map[string]bool{}
	for id := range ids {
		seen[id] = true
	}
	if len(seen) != 1 || f.chargeCalls != 1 {
		t.Fatal("duplicate orders", len(seen), f.chargeCalls)
	}
	other := customer()
	other.ID = "u-cli-2"
	o, _, err := s.Create(context.Background(), other, "same", createInput())
	if err != nil || o.UserID != other.ID || f.chargeCalls != 2 {
		t.Fatal(err, o)
	}
}
func TestIdempotencyConflictAndDefensiveSnapshots(t *testing.T) {
	f := fake()
	s := newService(t, f, nil)
	o, _ := create(t, s, "key", createInput())
	input := createInput()
	input.Quantity = 1
	if _, _, err := s.Create(context.Background(), customer(), "key", input); err == nil || err.Status != 409 {
		t.Fatal(err)
	}
	o.TicketIDs[0] = "tampered"
	real, _ := s.Get(o.ID, customer().ID)
	if real.TicketIDs[0] == "tampered" {
		t.Fatal("mutable snapshot")
	}
	if _, err := s.Get(o.ID, "other"); err == nil || err.Status != 404 {
		t.Fatal(err)
	}
}
func TestLostPaymentResponseRecoversWithoutSecondCharge(t *testing.T) {
	f := fake()
	f.failCharge = 1
	f.chargeApplied = true
	s := newService(t, f, func(c *Config) { c.RecoveryInterval = 5 * time.Millisecond })
	o, _ := create(t, s, "lost", createInput())
	o = awaitCompleted(t, s, o.ID)
	if o.Status != "paid" || f.chargeCalls != 1 || len(f.payments) != 1 {
		t.Fatal(o, f.chargeCalls)
	}
}
func TestUnknownPayment404DoesNotReleaseOrCreateAnotherCharge(t *testing.T) {
	f := fake()
	f.failCharge = 1
	s := newService(t, f, nil)
	o, status := create(t, s, "unknown", createInput())
	if status != 202 || o.PaymentStatus != "unknown" || o.Status != "pending" || o.ProcessingStatus != "recovery_required" || f.releaseCalls != 0 {
		t.Fatal(o, status)
	}
	before := f.chargeCalls
	_, _ = s.Get(o.ID, customer().ID)
	if f.chargeCalls != before {
		t.Fatal("GET restarted charge")
	}
	again, status := create(t, s, "unknown", createInput())
	if status != 200 || again.ID != o.ID || again.Status != "paid" || len(f.payments) != 1 {
		t.Fatal(again, status)
	}
}
func TestIssuedButLostResponseRecoversSameTickets(t *testing.T) {
	f := fake()
	f.failIssue = 1
	f.issueApplied = true
	s := newService(t, f, nil)
	o, status := create(t, s, "issue", createInput())
	if status != 202 || o.Status != "paid" || o.TicketIssuanceStatus != "failed" || f.releaseCalls != 0 {
		t.Fatal(o, status)
	}
	again, status := create(t, s, "issue", createInput())
	if status != 200 || again.ID != o.ID || len(again.TicketIDs) != 2 || len(f.tickets) != 1 || f.chargeCalls != 1 {
		t.Fatal(again, status)
	}
}
func TestReleaseAndConfirmationFailuresRemainRecoverable(t *testing.T) {
	for _, kind := range []string{"release", "confirm"} {
		t.Run(kind, func(t *testing.T) {
			f := fake()
			input := createInput()
			if kind == "release" {
				f.failRelease = 1
				input.Card.Number = "4000000000000000"
			} else {
				f.failConfirm = 1
			}
			s := newService(t, f, nil)
			o, status := create(t, s, kind, input)
			if status != 202 || o.ProcessingStatus != "recovery_required" {
				t.Fatal(o, status)
			}
			if kind == "release" && o.Status != "rejected" {
				t.Fatal(o)
			}
			if kind == "confirm" && (o.Status != "paid" || f.issueCalls != 0) {
				t.Fatal(o)
			}
			again, status := create(t, s, kind, input)
			if status != 200 || again.ProcessingStatus != "completed" || f.chargeCalls != 1 {
				t.Fatal(again, status)
			}
		})
	}
}
func TestReservationResponseLostNoUnsafeBackgroundCharge(t *testing.T) {
	f := fake()
	f.failReserve = 1
	f.reserveApplied = true
	s := newService(t, f, func(c *Config) { c.RecoveryInterval = 5 * time.Millisecond })
	o, _ := create(t, s, "reserve", createInput())
	time.Sleep(30 * time.Millisecond)
	f.mu.Lock()
	charges := f.chargeCalls
	count := len(f.quotes)
	f.mu.Unlock()
	if charges != 0 || count != 1 {
		t.Fatal("unsafe recovery", charges, count)
	}
	again, status := create(t, s, "reserve", createInput())
	if status != 200 || again.ID != o.ID || again.Total != 17000 {
		t.Fatal(again, status)
	}
}
func TestNotificationsFailureDoesNotInvalidatePurchase(t *testing.T) {
	f := fake()
	s := newService(t, f, nil)
	o, status := create(t, s, "mail", createInput())
	if status != 201 || o.Status != "paid" || o.EmailSent || o.ProcessingStatus != "completed" {
		t.Fatal(o, status)
	}
}
func TestBoundedOperationReturns202AndGetHasNoSideEffects(t *testing.T) {
	f := fake()
	f.delayReserve = time.Second
	s := newService(t, f, func(c *Config) { c.OperationTimeout = 20 * time.Millisecond })
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	start := time.Now()
	o, status, err := s.Create(ctx, customer(), "slow", createInput())
	if err != nil || status != 202 || o.ID == "" || time.Since(start) > 500*time.Millisecond {
		t.Fatal(o, status, err)
	}
	if _, err = s.Get(o.ID, customer().ID); err != nil {
		t.Fatal(err)
	}
}
func TestSalesUsePaidHistoricalTotalsAndBoliviaDays(t *testing.T) {
	f := fake()
	s := newService(t, f, nil)
	o, _ := create(t, s, "sales", createInput())
	f.mu.Lock()
	f.event.Sectors[0].Price = 99900
	f.mu.Unlock()
	stats, err := s.Stats(context.Background(), User{ID: "u-org-1", Role: "organizer"}, "e-demo")
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 1 || stats[0].Revenue != o.Total || stats[0].TicketsSold != 2 || stats[0].BySector[0].Price != 99900 || len(stats[0].ByDay) != 14 || stats[0].ByDay[13].Date != "2030-01-01" || stats[0].ByDay[13].Sold != 2 {
		t.Fatal(stats)
	}
	if _, err = s.Stats(context.Background(), User{ID: "u-org-2", Role: "organizer"}, "e-demo"); err == nil || err.Status != 404 {
		t.Fatal(err)
	}
}

func TestStatsIncludeZeroSalesAndRejectWrongRole(t *testing.T) {
	f := fake()
	s := newService(t, f, nil)
	stats, err := s.Stats(context.Background(), User{ID: "u-org-1", Role: "organizer"}, "")
	if err != nil || len(stats) != 1 || stats[0].Revenue != 0 || stats[0].TicketsSold != 0 || len(stats[0].ByDay) != 14 {
		t.Fatal(stats, err)
	}
	if _, err = s.Stats(context.Background(), customer(), ""); err == nil || err.Status != 403 {
		t.Fatal(err)
	}
}
func TestConfigBoundsAndExpiredEventPreflight(t *testing.T) {
	for _, change := range []func(*Config){func(c *Config) { c.OperationTimeout = 7 * time.Second }, func(c *Config) { c.HTTPTimeout = 4 * time.Second }, func(c *Config) { c.JWTSecret = "short" }, func(c *Config) { c.EventsURL = "http://user:secret@localhost" }, func(c *Config) { c.Port = 0 }} {
		config := testConfig()
		change(&config)
		if config.Validate() == nil {
			t.Fatal("accepted invalid config")
		}
	}
	f := fake()
	f.event.Date = testClock().Add(-time.Hour)
	s := newService(t, f, nil)
	if _, _, err := s.Create(context.Background(), customer(), "past", createInput()); err == nil || err.Status != 404 {
		t.Fatal(err)
	}
	if f.reserveCalls != 0 {
		t.Fatal("reserved closed event")
	}
}
