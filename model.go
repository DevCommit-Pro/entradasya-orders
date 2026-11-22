package orders

import "time"

type Card struct {
	Number string `json:"number"`
	Holder string `json:"holder"`
	Expiry string `json:"expiry"`
	CVC    string `json:"cvc"`
}
type CreateInput struct {
	EventID  string `json:"eventId"`
	SectorID string `json:"sectorId"`
	Quantity int    `json:"quantity"`
	Card     Card   `json:"card"`
}
type Order struct {
	ID                   string    `json:"id"`
	UserID               string    `json:"userId"`
	EventID              string    `json:"eventId"`
	EventName            string    `json:"eventName"`
	EventDate            time.Time `json:"eventDate"`
	Venue                string    `json:"venue"`
	City                 string    `json:"city"`
	SectorID             string    `json:"sectorId"`
	SectorName           string    `json:"sectorName"`
	Quantity             int       `json:"quantity"`
	UnitPrice            Money     `json:"unitPrice"`
	Total                Money     `json:"total"`
	Status               string    `json:"status"`
	PaymentStatus        string    `json:"paymentStatus"`
	TicketIssuanceStatus string    `json:"ticketIssuanceStatus"`
	ProcessingStatus     string    `json:"processingStatus"`
	RejectionReason      *string   `json:"rejectionReason"`
	CardLast4            string    `json:"cardLast4"`
	EmailSent            bool      `json:"emailSent"`
	TicketIDs            []string  `json:"ticketIds"`
	CreatedAt            time.Time `json:"createdAt"`
}
type Sector struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Price     Money  `json:"price"`
	Capacity  uint32 `json:"capacity"`
	Available uint32 `json:"available"`
}
type Event struct {
	ID          string    `json:"id"`
	OrganizerID string    `json:"organizerId"`
	Name        string    `json:"name"`
	Date        time.Time `json:"date"`
	Venue       string    `json:"venue"`
	City        string    `json:"city"`
	Status      string    `json:"status"`
	Sectors     []Sector  `json:"sectors"`
}

type Quote struct {
	Reserved          bool      `json:"reserved"`
	OrderID           string    `json:"orderId"`
	EventID           string    `json:"eventId"`
	SectorID          string    `json:"sectorId"`
	Quantity          int       `json:"quantity"`
	UnitPrice         Money     `json:"unitPrice"`
	Total             Money     `json:"total"`
	Currency          string    `json:"currency"`
	ReservationStatus string    `json:"reservationStatus"`
	EventName         string    `json:"eventName"`
	EventDate         time.Time `json:"eventDate"`
	Venue             string    `json:"venue"`
	City              string    `json:"city"`
	SectorName        string    `json:"sectorName"`
}
type Payment struct {
	TransactionID string    `json:"transactionId"`
	OrderID       string    `json:"orderId"`
	Status        string    `json:"status"`
	Amount        Money     `json:"amount"`
	Currency      string    `json:"currency"`
	CardLast4     string    `json:"cardLast4"`
	CreatedAt     time.Time `json:"createdAt"`
}
type Ticket struct {
	ID      string `json:"id"`
	Code    string `json:"code"`
	OrderID string `json:"orderId"`
	UserID  string `json:"userId"`
	EventID string `json:"eventId"`
	Status  string `json:"status"`
}
type IssueInput struct {
	OrderID    string    `json:"orderId"`
	UserID     string    `json:"userId"`
	HolderName string    `json:"holderName"`
	EventID    string    `json:"eventId"`
	EventName  string    `json:"eventName"`
	EventDate  time.Time `json:"eventDate"`
	Venue      string    `json:"venue"`
	City       string    `json:"city"`
	SectorName string    `json:"sectorName"`
	Quantity   int       `json:"quantity"`
}
