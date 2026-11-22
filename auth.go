package orders

import (
	"github.com/golang-jwt/jwt/v5"
	"net/http"
	"strings"
)

type User struct{ ID, Name, Email, Role string }
type tokenClaims struct {
	jwt.RegisteredClaims
	Name  string `json:"name"`
	Email string `json:"email"`
	Role  string `json:"role"`
}

func authenticate(r *http.Request, secret string) (User, *APIError) {
	if len(r.Header.Values("Authorization")) != 1 {
		return User{}, unauthorized()
	}
	fields := strings.Fields(r.Header.Get("Authorization"))
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") {
		return User{}, unauthorized()
	}
	claims := &tokenClaims{}
	token, err := jwt.ParseWithClaims(fields[1], claims, func(*jwt.Token) (any, error) { return []byte(secret), nil }, jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired(), jwt.WithIssuedAt())
	if err != nil || !token.Valid || claims.IssuedAt == nil || claims.ExpiresAt == nil || (claims.IssuedAt.Unix() < 0 || !claims.ExpiresAt.After(claims.IssuedAt.Time)) || strings.TrimSpace(claims.Subject) == "" || strings.TrimSpace(claims.Name) == "" || strings.TrimSpace(claims.Email) == "" || len(claims.Audience) != 0 {
		return User{}, unauthorized()
	}
	if claims.Role != "customer" && claims.Role != "organizer" && claims.Role != "doorman" {
		return User{}, unauthorized()
	}
	return User{ID: claims.Subject, Name: claims.Name, Email: claims.Email, Role: claims.Role}, nil
}
