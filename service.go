package orders

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type attempt struct {
	ready chan struct{}
	id    string
	err   *APIError
}
type intentScope struct{ userID, key string }

type intent struct {
	fingerprint [32]byte
	attempt     *attempt
}
type record struct {
	order                         Order
	user                          User
	quoted, confirmed, dispatched bool
	active                        bool
	done                          chan struct{}
}
type Service struct {
	config         Config
	backend        Backend
	clock          func() time.Time
	mu             sync.Mutex
	orders         map[string]*record
	intents        map[intentScope]*intent
	fingerprintKey [32]byte
	root           context.Context
	cancel         context.CancelFunc
	wg             sync.WaitGroup
	closed         bool
}

func New(config Config, backend Backend, clock func() time.Time) (*Service, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if backend == nil {
		return nil, errors.New("falta backend")
	}
	if clock == nil {
		clock = time.Now
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{config: config, backend: backend, clock: clock, orders: map[string]*record{}, intents: map[intentScope]*intent{}, root: ctx, cancel: cancel}
	if _, err := rand.Read(s.fingerprintKey[:]); err != nil {
		cancel()
		return nil, errors.New("no se pudo inicializar orders")
	}
	s.wg.Add(1)
	go s.recoverLoop()
	return s, nil
}
func (s *Service) Close()    { s.mu.Lock(); s.closed = true; s.cancel(); s.mu.Unlock(); s.wg.Wait() }
func validID(id string) bool { return strings.TrimSpace(id) != "" && len(id) <= 128 }
func digits(value string, min, max int) bool {
	if len(value) < min || len(value) > max {
		return false
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
func normalize(input CreateInput) (CreateInput, *APIError) {
	if input.Quantity < 1 || input.Quantity > 6 {
		return input, &APIError{400, "invalid_quantity", "Puedes comprar entre 1 y 6 entradas por compra."}
	}
	input.Card.Holder = strings.TrimSpace(input.Card.Holder)
	card := input.Card
	if !validID(input.EventID) || !validID(input.SectorID) || !digits(card.Number, 13, 19) || !digits(card.CVC, 3, 4) || card.Holder == "" || len(card.Holder) > 320 || len(card.Expiry) != 5 || card.Expiry[2] != '/' || !digits(card.Expiry[:2], 2, 2) || !digits(card.Expiry[3:], 2, 2) {
		return input, invalid()
	}
	month, _ := strconv.Atoi(card.Expiry[:2])
	if month < 1 || month > 12 {
		return input, invalid()
	}
	return input, nil
}
func (s *Service) fingerprint(input CreateInput) [32]byte {
	digest := hmac.New(sha256.New, s.fingerprintKey[:])
	for _, value := range []string{input.EventID, input.SectorID, strconv.Itoa(input.Quantity), input.Card.Number, input.Card.Holder, input.Card.Expiry, input.Card.CVC} {
		_, _ = io.WriteString(digest, strconv.Itoa(len(value))+":"+value)
	}
	var result [32]byte
	copy(result[:], digest.Sum(nil))
	return result
}
func clone(order Order) Order {
	order.TicketIDs = append([]string{}, order.TicketIDs...)
	if order.RejectionReason != nil {
		reason := *order.RejectionReason
		order.RejectionReason = &reason
	}
	return order
}
func (s *Service) Get(id, user string) (Order, *APIError) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.orders[id]
	if !ok || r.order.UserID != user {
		return Order{}, missing()
	}
	return clone(r.order), nil
}
func (s *Service) Mine(user string) []Order {
	s.mu.Lock()
	defer s.mu.Unlock()
	orders := []Order{}
	for _, r := range s.orders {
		if r.order.UserID == user {
			orders = append(orders, clone(r.order))
		}
	}
	sort.Slice(orders, func(i, j int) bool {
		if orders[i].CreatedAt.Equal(orders[j].CreatedAt) {
			return orders[i].ID > orders[j].ID
		}
		return orders[i].CreatedAt.After(orders[j].CreatedAt)
	})
	return orders
}
func (s *Service) Create(ctx context.Context, user User, key string, input CreateInput) (Order, int, *APIError) {
	if user.Role != "customer" {
		return Order{}, 0, forbidden()
	}
	if !validID(key) {
		return Order{}, 0, invalid()
	}
	var invalidInput *APIError
	input, invalidInput = normalize(input)
	if invalidInput != nil {
		return Order{}, 0, invalidInput
	}
	fingerprint := s.fingerprint(input)
	scope := intentScope{userID: user.ID, key: key}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return Order{}, 0, unavailable()
	}
	existing := s.intents[scope]
	fresh := false
	if existing != nil && !hmac.Equal(existing.fingerprint[:], fingerprint[:]) {
		s.mu.Unlock()
		return Order{}, 0, &APIError{409, "idempotency_conflict", "La clave ya corresponde a otra solicitud de compra."}
	}
	if existing == nil {
		existing = &intent{fingerprint: fingerprint}
		s.intents[scope] = existing
	}
	restart := existing.attempt == nil
	if !restart {
		select {
		case <-existing.attempt.ready:
			restart = existing.attempt.id == "" && existing.attempt.err != nil && existing.attempt.err.Status == 503
		default:
		}
	}
	if restart {
		existing.attempt = &attempt{ready: make(chan struct{})}
		fresh = true
		s.wg.Add(1)
		go s.prepare(existing.attempt, user, input)
	}
	a := existing.attempt
	s.mu.Unlock()
	select {
	case <-a.ready:
	case <-ctx.Done():
		select {
		case <-a.ready:
		default:
			return Order{}, 0, unavailable()
		}
	}
	if a.err != nil {
		return Order{}, 0, a.err
	}
	s.mu.Lock()
	r := s.orders[a.id]
	resumed := false
	if !fresh && !r.active && r.order.ProcessingStatus != "completed" && !s.closed {
		r.active = true
		resumed = true
		r.done = make(chan struct{})
		s.wg.Add(1)
		card := input.Card
		go s.work(a.id, &card)
	}
	done := r.done
	s.mu.Unlock()
	select {
	case <-done:
	case <-ctx.Done():
	}
	// A concurrent recovery job may have finished without card data. The caller
	// still has its identical transient payload and may resume once after it.
	if !fresh && !resumed && ctx.Err() == nil {
		s.mu.Lock()
		r = s.orders[a.id]
		if !r.active && r.order.ProcessingStatus != "completed" && !s.closed {
			r.active = true
			r.done = make(chan struct{})
			s.wg.Add(1)
			card := input.Card
			go s.work(a.id, &card)
			done = r.done
		} else {
			done = r.done
		}
		s.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
		}
	}
	order, err := s.Get(a.id, user.ID)
	if err != nil {
		return Order{}, 0, err
	}
	status := 200
	if fresh {
		status = 201
	}
	if order.ProcessingStatus != "completed" {
		status = 202
	}
	return order, status, nil
}
func (s *Service) prepare(a *attempt, user User, input CreateInput) {
	defer s.wg.Done()
	defer func() {
		if recover() != nil {
			slog.Error("order_preflight_failed")
			s.mu.Lock()
			if a.id == "" {
				a.err = unavailable()
				select {
				case <-a.ready:
				default:
					close(a.ready)
				}
			}
			s.mu.Unlock()
		}
	}()
	ctx, cancel := context.WithTimeout(s.root, s.config.OperationTimeout)
	defer cancel()
	event, err := s.backend.Event(ctx, input.EventID)
	var failure *APIError
	if err != nil {
		var upstream *UpstreamError
		if errors.As(err, &upstream) && upstream.Status == 404 {
			failure = missing()
		} else {
			failure = unavailable()
		}
	}
	if failure == nil && (event.ID != input.EventID || event.OrganizerID == "" || event.Name == "" || event.Date.IsZero() || event.Venue == "" || event.City == "") {
		failure = unavailable()
	}
	if failure == nil && (event.Status != "published" || !event.Date.After(s.clock())) {
		failure = missing()
	}
	var sector *Sector
	if failure == nil {
		for i := range event.Sectors {
			if event.Sectors[i].ID == input.SectorID {
				sector = &event.Sectors[i]
				break
			}
		}
		if sector == nil {
			failure = missing()
		} else if sector.Price < 0 || int64(sector.Price) > MaxCents/6 || sector.Name == "" {
			failure = unavailable()
		}
	}
	if ctx.Err() != nil {
		failure = unavailable()
	}
	if failure != nil {
		s.mu.Lock()
		a.err = failure
		close(a.ready)
		s.mu.Unlock()
		return
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		s.mu.Lock()
		a.err = unavailable()
		close(a.ready)
		s.mu.Unlock()
		return
	}
	id := "o-" + hex.EncodeToString(random[:])
	o := Order{ID: id, UserID: user.ID, EventID: event.ID, EventName: event.Name, EventDate: event.Date, Venue: event.Venue, City: event.City, SectorID: sector.ID, SectorName: sector.Name, Quantity: input.Quantity, UnitPrice: sector.Price, Total: sector.Price * Money(input.Quantity), Status: "pending", PaymentStatus: "not_started", TicketIssuanceStatus: "not_started", ProcessingStatus: "processing", CardLast4: strings.Clone(input.Card.Number[len(input.Card.Number)-4:]), TicketIDs: []string{}, CreatedAt: s.clock().UTC()}
	s.mu.Lock()
	s.orders[id] = &record{order: o, user: user, active: true, done: make(chan struct{})}
	a.id = id
	close(a.ready)
	s.mu.Unlock()
	defer s.finish(id)
	s.run(ctx, id, &input.Card)
}
func (s *Service) work(id string, card *Card) {
	defer s.wg.Done()
	defer s.finish(id)
	ctx, cancel := context.WithTimeout(s.root, s.config.OperationTimeout)
	defer cancel()
	s.run(ctx, id, card)
}
func (s *Service) finish(id string) {
	if recover() != nil {
		slog.Error("order_worker_failed")
	}
	s.mu.Lock()
	r := s.orders[id]
	if r.order.ProcessingStatus != "completed" {
		r.order.ProcessingStatus = "recovery_required"
	}
	r.active = false
	close(r.done)
	s.mu.Unlock()
}
func (s *Service) snapshot(id string) record {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := *s.orders[id]
	r.order = clone(r.order)
	return r
}
func (s *Service) update(id string, update func(*record)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	update(s.orders[id])
}
func validQuote(q Quote, o Order) bool {
	return q.Reserved && q.OrderID == o.ID && q.EventID == o.EventID && q.SectorID == o.SectorID && q.Quantity == o.Quantity && q.Currency == "BOB" && q.UnitPrice >= 0 && int64(q.UnitPrice) <= MaxCents/6 && q.Total == q.UnitPrice*Money(o.Quantity) && (q.ReservationStatus == "reserved" || q.ReservationStatus == "confirmed") && q.EventName != "" && !q.EventDate.IsZero() && q.Venue != "" && q.City != "" && q.SectorName != ""
}
func validPayment(p Payment, o Order) bool {
	return p.OrderID == o.ID && p.TransactionID != "" && (p.Status == "approved" || p.Status == "declined") && p.Amount == o.Total && p.Currency == "BOB" && p.CardLast4 == o.CardLast4 && !p.CreatedAt.IsZero()
}
func (s *Service) run(ctx context.Context, id string, card *Card) {
	r := s.snapshot(id)
	if !r.quoted {
		quote, err := s.backend.Reserve(ctx, r.order)
		if err != nil {
			var upstream *UpstreamError
			if errors.As(err, &upstream) && upstream.Status == 409 && upstream.Code == "sold_out" {
				s.update(id, func(r *record) {
					reason := "sold_out"
					r.order.Status = "rejected"
					r.order.RejectionReason = &reason
					r.order.ProcessingStatus = "completed"
				})
			}
			return
		}
		if !validQuote(quote, r.order) {
			return
		}
		s.update(id, func(r *record) {
			r.quoted = true
			r.confirmed = quote.ReservationStatus == "confirmed"
			r.order.UnitPrice = quote.UnitPrice
			r.order.Total = quote.Total
			r.order.EventName = quote.EventName
			r.order.EventDate = quote.EventDate
			r.order.Venue = quote.Venue
			r.order.City = quote.City
			r.order.SectorName = quote.SectorName
			if r.confirmed {
				r.dispatched = true
				r.order.PaymentStatus = "unknown"
			}
		})
	}
	r = s.snapshot(id)
	if r.order.PaymentStatus != "approved" && r.order.PaymentStatus != "declined" {
		var payment Payment
		var err error
		if r.dispatched {
			payment, err = s.backend.Payment(ctx, id)
		}
		if !r.dispatched || (err != nil && isNotFound(err) && card != nil && !r.confirmed) {
			if card == nil {
				return
			}
			s.update(id, func(r *record) { r.dispatched = true; r.order.PaymentStatus = "pending" })
			payment, err = s.backend.Charge(ctx, r.order, *card)
		}
		if err != nil || !validPayment(payment, r.order) {
			s.update(id, func(r *record) {
				if r.dispatched {
					r.order.PaymentStatus = "unknown"
				}
			})
			return
		}
		s.update(id, func(r *record) {
			if payment.Status == "approved" {
				r.order.PaymentStatus = "approved"
				r.order.Status = "paid"
			} else {
				reason := "payment_declined"
				r.order.PaymentStatus = "declined"
				r.order.Status = "rejected"
				r.order.RejectionReason = &reason
			}
		})
	}
	r = s.snapshot(id)
	if r.order.PaymentStatus == "declined" {
		if s.backend.Release(ctx, r.order) != nil {
			return
		}
		s.update(id, func(r *record) { r.order.ProcessingStatus = "completed" })
		return
	}
	if r.order.PaymentStatus != "approved" {
		return
	}
	if !r.confirmed {
		if s.backend.Confirm(ctx, r.order) != nil {
			return
		}
		s.update(id, func(r *record) { r.confirmed = true })
	}
	r = s.snapshot(id)
	if r.order.TicketIssuanceStatus == "issued" {
		s.update(id, func(r *record) { r.order.ProcessingStatus = "completed" })
		return
	}
	s.update(id, func(r *record) { r.order.TicketIssuanceStatus = "pending" })
	input := IssueInput{OrderID: id, UserID: r.order.UserID, HolderName: r.user.Name, EventID: r.order.EventID, EventName: r.order.EventName, EventDate: r.order.EventDate, Venue: r.order.Venue, City: r.order.City, SectorName: r.order.SectorName, Quantity: r.order.Quantity}
	tickets, err := s.backend.Issue(ctx, input)
	ids := []string{}
	seen := map[string]bool{}
	if err == nil && len(tickets) == r.order.Quantity {
		for _, ticket := range tickets {
			if ticket.ID == "" || ticket.Code == "" || ticket.OrderID != id || ticket.UserID != r.order.UserID || ticket.EventID != r.order.EventID || seen[ticket.ID] || (ticket.Status != "valid" && ticket.Status != "used" && ticket.Status != "cancelled") {
				err = errors.New("invalid issuance")
				break
			}
			seen[ticket.ID] = true
			ids = append(ids, ticket.ID)
		}
	} else {
		err = errors.New("incomplete issuance")
	}
	if err != nil {
		s.update(id, func(r *record) { r.order.TicketIssuanceStatus = "failed" })
		return
	}
	s.update(id, func(r *record) { r.order.TicketIDs = ids; r.order.TicketIssuanceStatus = "issued" })
	notifyCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	sent := false
	if notifyCtx.Err() == nil {
		sent = s.backend.Notify(notifyCtx, s.snapshot(id).order, r.user, tickets)
	}
	s.update(id, func(r *record) { r.order.EmailSent = sent; r.order.ProcessingStatus = "completed" })
}
func isNotFound(err error) bool {
	var upstream *UpstreamError
	return errors.As(err, &upstream) && upstream.Status == 404
}
func (s *Service) recoverLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(s.config.RecoveryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.root.Done():
			return
		case <-ticker.C:
			s.mu.Lock()
			if !s.closed {
				started := 0
				for id, r := range s.orders {
					if started >= 16 {
						break
					}
					if !r.active && r.order.ProcessingStatus != "completed" && (!r.quoted || r.dispatched || r.order.Status == "paid" || r.order.PaymentStatus == "declined") {
						r.active = true
						r.done = make(chan struct{})
						s.wg.Add(1)
						go s.work(id, nil)
						started++
					}
				}
			}
			s.mu.Unlock()
		}
	}
}
