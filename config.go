package orders

import (
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Port             int
	JWTSecret        string
	InternalAPIKey   string
	EventsURL        string
	PaymentsURL      string
	TicketsURL       string
	NotificationsURL string
	HTTPTimeout      time.Duration
	OperationTimeout time.Duration
	RecoveryInterval time.Duration
}

func validOrigin(value string) bool {
	u, err := url.Parse(value)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil && (u.Path == "" || u.Path == "/") && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && !strings.Contains(value, "#")
}
func (c Config) Validate() error {
	if c.Port < 1 || c.Port > 65535 || len(c.JWTSecret) < 32 || len(c.InternalAPIKey) < 32 {
		return errors.New("puerto o secretos no válidos")
	}
	if !validOrigin(c.EventsURL) || !validOrigin(c.PaymentsURL) || !validOrigin(c.TicketsURL) || (c.NotificationsURL != "" && !validOrigin(c.NotificationsURL)) {
		return errors.New("URLs de servicios no válidas")
	}
	if c.HTTPTimeout <= 0 || c.HTTPTimeout > 3*time.Second || c.OperationTimeout <= 0 || c.OperationTimeout > 6*time.Second || c.RecoveryInterval <= 0 {
		return errors.New("timeouts no válidos")
	}
	return nil
}
func ConfigFromEnv() (Config, error) {
	config := Config{Port: 4003, JWTSecret: os.Getenv("JWT_SECRET"), InternalAPIKey: os.Getenv("INTERNAL_API_KEY"), EventsURL: os.Getenv("EVENTS_URL"), PaymentsURL: os.Getenv("PAYMENTS_URL"), TicketsURL: os.Getenv("TICKETS_URL"), NotificationsURL: os.Getenv("NOTIFICATIONS_URL"), HTTPTimeout: 2 * time.Second, OperationTimeout: 6 * time.Second, RecoveryInterval: time.Second}
	if v, ok := os.LookupEnv("PORT"); ok {
		parsed, err := strconv.Atoi(v)
		if err != nil {
			return Config{}, errors.New("PORT no válido")
		}
		config.Port = parsed
	}
	for name, target := range map[string]*time.Duration{"HTTP_TIMEOUT_MS": &config.HTTPTimeout, "OPERATION_TIMEOUT_MS": &config.OperationTimeout, "RECOVERY_INTERVAL_MS": &config.RecoveryInterval} {
		if v, ok := os.LookupEnv(name); ok {
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 || n > 60000 {
				return Config{}, errors.New("duración de entorno no válida")
			}
			*target = time.Duration(n) * time.Millisecond
		}
	}
	return config, config.Validate()
}
