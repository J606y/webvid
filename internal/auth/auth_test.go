package auth

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestSignParseTokenRoundTrip(t *testing.T) {
	secret := []byte("test-secret")
	tok, exp, err := SignToken(42, secret)
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}
	if tok == "" {
		t.Fatal("SignToken returned empty token")
	}
	if exp.Before(time.Now()) {
		t.Fatalf("exp should be in the future, got %v", exp)
	}
	id, err := ParseToken(tok, secret)
	if err != nil {
		t.Fatalf("ParseToken: %v", err)
	}
	if id != 42 {
		t.Fatalf("got userID %d want 42", id)
	}
}

// TestParseTokenAlgConfusion 是本文件最重要的用例：ParseToken 必须只接受 HS256，
// 拒绝篡改、错误 secret、alg=none、以及非 HS256（如 HS384）签发的 token。
func TestParseTokenAlgConfusion(t *testing.T) {
	secret := []byte("correct-secret")

	t.Run("tampered token rejected", func(t *testing.T) {
		tok, _, err := SignToken(1, secret)
		if err != nil {
			t.Fatalf("SignToken: %v", err)
		}
		tampered := tok[:len(tok)-1] + flipLastChar(tok[len(tok)-1])
		if _, err := ParseToken(tampered, secret); err == nil {
			t.Fatal("ParseToken 应拒绝被篡改的 token")
		}
	})

	t.Run("wrong secret rejected", func(t *testing.T) {
		tok, _, err := SignToken(1, secret)
		if err != nil {
			t.Fatalf("SignToken: %v", err)
		}
		if _, err := ParseToken(tok, []byte("wrong-secret")); err == nil {
			t.Fatal("ParseToken 应拒绝用错误 secret 签的 token")
		}
	})

	t.Run("alg=none rejected", func(t *testing.T) {
		noneTok := buildUnsignedNoneToken(t, jwt.MapClaims{
			"sub": "1",
			"exp": time.Now().Add(time.Hour).Unix(),
		})
		if _, err := ParseToken(noneTok, secret); err == nil {
			t.Fatal("ParseToken 应拒绝 alg=none 的 token")
		}
	})

	t.Run("non-HS256 alg rejected", func(t *testing.T) {
		// 用 HS384 签发（同样是 HMAC 家族，密钥可复用 secret），ParseToken 限定
		// WithValidMethods([]string{"HS256"})，应拒绝。
		tok := jwt.NewWithClaims(jwt.SigningMethodHS384, jwt.MapClaims{
			"sub": "1",
			"exp": time.Now().Add(time.Hour).Unix(),
		})
		s, err := tok.SignedString(secret)
		if err != nil {
			t.Fatalf("签发 HS384 token: %v", err)
		}
		if _, err := ParseToken(s, secret); err == nil {
			t.Fatal("ParseToken 应拒绝非 HS256 算法签发的 token")
		}
	})
}

func TestParseTokenExpired(t *testing.T) {
	secret := []byte("expiry-secret")
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "1",
		"exp": time.Now().Add(-time.Hour).Unix(), // 已过期
	})
	s, err := tok.SignedString(secret)
	if err != nil {
		t.Fatalf("签发过期 token: %v", err)
	}
	if _, err := ParseToken(s, secret); err == nil {
		t.Fatal("ParseToken 应拒绝已过期的 token")
	}
}

func TestHashVerifyPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("s3cr3t!")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !VerifyPassword(hash, "s3cr3t!") {
		t.Fatal("VerifyPassword 对正确密码应返回 true")
	}
	if VerifyPassword(hash, "wrong-password") {
		t.Fatal("VerifyPassword 对错误密码应返回 false")
	}
}

// buildUnsignedNoneToken 手工构造 alg=none、无签名段的 JWT（经典 alg-confusion 攻击载荷）。
func buildUnsignedNoneToken(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodNone, claims)
	s, err := tok.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("构造 alg=none token: %v", err)
	}
	return s
}

// flipLastChar 返回一个与 c 不同的 base64url 字符，用于制造签名篡改。
func flipLastChar(c byte) string {
	if c == 'A' {
		return "B"
	}
	return "A"
}
