package auth

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

const TokenTTL = 48 * time.Hour

var ErrInvalidToken = errors.New("invalid token")

func HashPassword(plain string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plain), 10)
	return string(b), err
}

func VerifyPassword(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}

// RandomPassword 生成 n 位随机密码（字母数字，crypto/rand）。
func RandomPassword(n int) string {
	const chars = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	for i := range buf {
		buf[i] = chars[int(buf[i])%len(chars)]
	}
	return string(buf)
}

// SignToken 签发 HS256 JWT，sub 为用户 ID。
func SignToken(userID int64, secret []byte) (string, time.Time, error) {
	exp := time.Now().Add(TokenTTL)
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": strconv.FormatInt(userID, 10),
		"iat": time.Now().Unix(),
		"exp": exp.Unix(),
	})
	s, err := t.SignedString(secret)
	return s, exp, err
}

// ParseToken 校验并返回用户 ID；仅接受 HS256。
func ParseToken(tokenStr string, secret []byte) (int64, error) {
	tok, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
		return secret, nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil || !tok.Valid {
		return 0, ErrInvalidToken
	}
	claims, ok := tok.Claims.(jwt.MapClaims)
	if !ok {
		return 0, ErrInvalidToken
	}
	sub, err := claims.GetSubject()
	if err != nil {
		return 0, ErrInvalidToken
	}
	id, err := strconv.ParseInt(sub, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: bad subject", ErrInvalidToken)
	}
	return id, nil
}
