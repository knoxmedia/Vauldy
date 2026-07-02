package auth

import (
	"errors"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type Claims struct {
	UserID   int64  `json:"uid"`
	Username string `json:"username"`
	Role     string `json:"role"`
	// AppID is set for machine tokens issued via OAuth2 client_credentials (api_client role).
	AppID int64 `json:"aid,omitempty"`
	jwt.RegisteredClaims
}

func SignToken(secret string, hours int, userID int64, username, role string) (string, error) {
	if hours <= 0 {
		hours = 168
	}
	claims := Claims{
		UserID:   userID,
		Username: username,
		Role:     role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Duration(hours) * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return t.SignedString([]byte(secret))
}

// SignClientToken issues a JWT for an OAuth2-style API client. Username is set to the public client_id for audit logs.
func SignClientToken(secret string, hours int, appID int64, clientID string) (string, error) {
	if hours <= 0 {
		hours = 168
	}
	claims := Claims{
		UserID:   0,
		Username: clientID,
		Role:     "api_client",
		AppID:    appID,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "api_client:" + strconv.FormatInt(appID, 10),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Duration(hours) * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return t.SignedString([]byte(secret))
}

func ParseToken(secret, token string) (*Claims, error) {
	t, err := jwt.ParseWithClaims(token, &Claims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil, err
	}
	c, ok := t.Claims.(*Claims)
	if !ok || !t.Valid {
		return nil, errors.New("invalid token")
	}
	return c, nil
}
