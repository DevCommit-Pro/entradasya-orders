package orders

import (
	"context"
	"sort"
	"time"
)

type SectorSales struct {
	SectorID   string `json:"sectorId"`
	SectorName string `json:"sectorName"`
	Price      Money  `json:"price"`
	Sold       int    `json:"sold"`
	Capacity   uint32 `json:"capacity"`
	Revenue    Money  `json:"revenue"`
}
type DailySales struct {
	Date string `json:"date"`
	Sold int    `json:"sold"`
}
type SalesStats struct {
	EventID     string        `json:"eventId"`
	EventName   string        `json:"eventName"`
	EventDate   time.Time     `json:"eventDate"`
	Status      string        `json:"status"`
	TicketsSold int           `json:"ticketsSold"`
	Capacity    uint64        `json:"capacity"`
	Revenue     Money         `json:"revenue"`
	BySector    []SectorSales `json:"bySector"`
	ByDay       []DailySales  `json:"byDay"`
}

var bolivia = time.FixedZone("Bolivia", -4*60*60)

func (s *Service) Stats(ctx context.Context, user User, eventID string) ([]SalesStats, *APIError) {
	if user.Role != "organizer" {
		return nil, forbidden()
	}
	var events []Event
	if eventID != "" {
		event, err := s.backend.Event(ctx, eventID)
		if err != nil {
			if isNotFound(err) {
				return nil, missing()
			}
			return nil, unavailable()
		}
		if event.ID != eventID {
			return nil, unavailable()
		}
		if event.OrganizerID != user.ID {
			return nil, missing()
		}
		events = []Event{event}
	} else {
		var err error
		events, err = s.backend.Events(ctx, user.ID)
		if err != nil {
			return nil, unavailable()
		}
	}
	s.mu.Lock()
	paid := []Order{}
	for _, r := range s.orders {
		if r.order.Status == "paid" {
			paid = append(paid, clone(r.order))
		}
	}
	s.mu.Unlock()
	result := make([]SalesStats, 0, len(events))
	seenEvents := map[string]bool{}
	for _, event := range events {
		if event.ID == "" || event.OrganizerID != user.ID || event.Name == "" || event.Date.IsZero() || len(event.Sectors) == 0 || seenEvents[event.ID] || (event.Status != "draft" && event.Status != "published" && event.Status != "cancelled") {
			return nil, unavailable()
		}
		seenEvents[event.ID] = true
		stats := SalesStats{EventID: event.ID, EventName: event.Name, EventDate: event.Date, Status: event.Status, BySector: []SectorSales{}, ByDay: []DailySales{}}
		sectorIndex := map[string]int{}
		for _, sector := range event.Sectors {
			if sector.ID == "" || sector.Capacity == 0 || sector.Price < 0 {
				return nil, unavailable()
			}
			if _, duplicate := sectorIndex[sector.ID]; duplicate {
				return nil, unavailable()
			}
			sectorIndex[sector.ID] = len(stats.BySector)
			stats.Capacity += uint64(sector.Capacity)
			if stats.Capacity > uint64(MaxCents) {
				return nil, unavailable()
			}
			stats.BySector = append(stats.BySector, SectorSales{SectorID: sector.ID, SectorName: sector.Name, Price: sector.Price, Capacity: sector.Capacity})
		}
		end := s.clock()
		if event.Date.Before(end) {
			end = event.Date
		}
		end = end.In(bolivia)
		end = time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, bolivia)
		dayIndex := map[string]int{}
		for offset := -13; offset <= 0; offset++ {
			date := end.AddDate(0, 0, offset).Format("2006-01-02")
			dayIndex[date] = len(stats.ByDay)
			stats.ByDay = append(stats.ByDay, DailySales{Date: date})
		}
		for _, order := range paid {
			if order.EventID != event.ID {
				continue
			}
			index, exists := sectorIndex[order.SectorID]
			if !exists {
				return nil, unavailable()
			}
			if int64(stats.Revenue) > MaxCents-int64(order.Total) {
				return nil, unavailable()
			}
			stats.Revenue += order.Total
			stats.TicketsSold += order.Quantity
			stats.BySector[index].Revenue += order.Total
			stats.BySector[index].Sold += order.Quantity
			if index, ok := dayIndex[order.CreatedAt.In(bolivia).Format("2006-01-02")]; ok {
				stats.ByDay[index].Sold += order.Quantity
			}
		}
		result = append(result, stats)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].EventDate.Equal(result[j].EventDate) {
			return result[i].EventID < result[j].EventID
		}
		return result[i].EventDate.Before(result[j].EventDate)
	})
	return result, nil
}
