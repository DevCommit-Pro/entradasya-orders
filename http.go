package orders

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"time"
)

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, err *APIError) {
	writeJSON(w, err.Status, map[string]any{"error": map[string]string{"code": err.Code, "message": err.Message}})
}
func parseCreate(w http.ResponseWriter, r *http.Request) (CreateInput, *APIError) {
	var input CreateInput
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return input, invalid()
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var wire struct {
		EventID  string          `json:"eventId"`
		SectorID string          `json:"sectorId"`
		Quantity json.RawMessage `json:"quantity"`
		Card     *Card           `json:"card"`
	}
	if decoder.Decode(&wire) != nil {
		return input, invalid()
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return input, invalid()
	}
	if json.Unmarshal(wire.Quantity, &input.Quantity) != nil || input.Quantity < 1 || input.Quantity > 6 {
		return input, &APIError{400, "invalid_quantity", "Puedes comprar entre 1 y 6 entradas por compra."}
	}
	if wire.Card == nil {
		return input, invalid()
	}
	input.EventID = wire.EventID
	input.SectorID = wire.SectorID
	input.Card = *wire.Card
	return normalize(input)
}
func (s *Service) createHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), s.config.OperationTimeout)
	defer cancel()
	user, err := authenticate(r, s.config.JWTSecret)
	if err != nil {
		writeError(w, err)
		return
	}
	if user.Role != "customer" {
		writeError(w, forbidden())
		return
	}
	values := r.Header.Values("Idempotency-Key")
	if len(values) != 1 || !validID(values[0]) {
		writeError(w, invalid())
		return
	}
	input, err := parseCreate(w, r)
	if err != nil {
		writeError(w, err)
		return
	}
	order, status, err := s.Create(ctx, user, values[0], input)
	input.Card.Number = ""
	input.Card.CVC = ""
	if err != nil {
		writeError(w, err)
		return
	}
	if status == 202 {
		w.Header().Set("Location", "/api/orders/"+order.ID)
	}
	writeJSON(w, status, order)
}
func (s *Service) mineHTTP(w http.ResponseWriter, r *http.Request) {
	user, err := authenticate(r, s.config.JWTSecret)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, s.Mine(user.ID))
}
func (s *Service) detailHTTP(w http.ResponseWriter, r *http.Request) {
	user, err := authenticate(r, s.config.JWTSecret)
	if err != nil {
		writeError(w, err)
		return
	}
	order, err := s.Get(r.PathValue("id"), user.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, order)
}
func (s *Service) statsHTTP(w http.ResponseWriter, r *http.Request) {
	user, err := authenticate(r, s.config.JWTSecret)
	if err != nil {
		writeError(w, err)
		return
	}
	if user.Role != "organizer" {
		writeError(w, forbidden())
		return
	}
	query := r.URL.Query()
	values, present := query["eventId"]
	id := ""
	if present {
		if len(values) != 1 || !validID(values[0]) {
			writeError(w, invalid())
			return
		}
		id = values[0]
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.config.OperationTimeout)
	defer cancel()
	stats, err := s.Stats(ctx, user, id)
	if err != nil {
		writeError(w, err)
		return
	}
	if present {
		if len(stats) != 1 {
			writeError(w, unavailable())
			return
		}
		writeJSON(w, 200, stats[0])
	} else {
		writeJSON(w, 200, stats)
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status != 0 {
		return
	}
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}
func (w *statusWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	return w.ResponseWriter.Write(data)
}
func (s *Service) Handler(logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	route := func(path, method string, handler http.HandlerFunc) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != method {
				w.Header().Set("Allow", method)
				writeError(w, &APIError{405, "invalid_request", "El método no está permitido."})
				return
			}
			handler(w, r)
		})
	}
	route("/health", "GET", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		closed := s.closed
		s.mu.Unlock()
		if closed {
			writeError(w, unavailable())
			return
		}
		writeJSON(w, 200, map[string]any{"status": "ok", "service": "orders", "storage": "memory", "time": s.clock().UTC(), "dependencies": "not_checked"})
	})
	route("/orders", "POST", s.createHTTP)
	route("/orders/mine", "GET", s.mineHTTP)
	route("/orders/stats", "GET", s.statsHTTP)
	route("/orders/{id}", "GET", s.detailHTTP)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { writeError(w, missing()) })
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		out := &statusWriter{ResponseWriter: w}
		defer func() {
			if recover() != nil {
				if out.status == 0 {
					writeError(out, &APIError{500, "internal_error", "No pudimos procesar la solicitud."})
				}
				if logger != nil {
					logger.Error("request_failed")
				}
			}
			if logger != nil {
				logger.Info("request_completed", "method", r.Method, "route", r.Pattern, "status", out.status, "elapsedMs", time.Since(started).Milliseconds())
			}
		}()
		mux.ServeHTTP(out, r)
	})
}
