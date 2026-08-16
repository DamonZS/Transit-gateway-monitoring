package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

const testSSOSecret = "0123456789abcdef0123456789abcdef"

func TestSSOExchangeIssuesExistingAdminTokenAndRejectsReplay(t *testing.T) {
	tokenIssuer, err := New("admin", "password", "admin-token-secret", time.Hour)
	if err != nil {
		t.Fatalf("New auth: %v", err)
	}
	sso, err := NewSSO(testSSOSecret, "toporeduce", "upstream-ops", "https://api.example.com/", tokenIssuer)
	if err != nil {
		t.Fatalf("NewSSO: %v", err)
	}
	now := time.Unix(1_800_000_000, 0)
	sso.now = func() time.Time { return now }
	assertion := signTestSSOAssertion(t, testSSOSecret, map[string]any{
		"iss": "toporeduce", "aud": "upstream-ops", "sub": "42", "role": 10,
		"nonce": "nonce-1", "jti": "jti-1", "iat": now.Unix(), "exp": now.Add(time.Minute).Unix(),
	})

	token, expiresAt, username, err := sso.Exchange(assertion, "nonce-1")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if token == "" || expiresAt.Before(time.Now()) || username != "admin" {
		t.Fatalf("exchange result token=%q expires=%s username=%q", token, expiresAt, username)
	}
	if subject, err := tokenIssuer.Verify(token); err != nil || subject != "admin" {
		t.Fatalf("issued token subject=%q err=%v", subject, err)
	}
	if _, _, _, err := sso.Exchange(assertion, "nonce-1"); !errors.Is(err, ErrSSOReplay) {
		t.Fatalf("replay error = %v", err)
	}

	public := sso.PublicConfig()
	if !public.Enabled || public.Issuer != "toporeduce" || public.Audience != DefaultSSOAudience || public.ParentOrigin != "https://api.example.com" {
		t.Fatalf("public config = %#v", public)
	}
}

func TestSSOExchangeRejectsInvalidClaims(t *testing.T) {
	tokenIssuer, err := New("admin", "password", "admin-token-secret", time.Hour)
	if err != nil {
		t.Fatalf("New auth: %v", err)
	}
	sso, err := NewSSO(testSSOSecret, "toporeduce", "upstream-ops", "https://api.example.com", tokenIssuer)
	if err != nil {
		t.Fatalf("NewSSO: %v", err)
	}
	now := time.Unix(1_800_000_000, 0)
	sso.now = func() time.Time { return now }

	valid := func(jti string) map[string]any {
		return map[string]any{
			"iss": "toporeduce", "aud": []string{"another-service", "upstream-ops"},
			"sub": "42", "role": 10, "nonce": "nonce", "jti": jti,
			"iat": now.Unix(), "exp": now.Add(time.Minute).Unix(),
		}
	}
	tests := []struct {
		name          string
		mutate        func(map[string]any)
		expectedNonce string
		secret        string
	}{
		{name: "issuer", mutate: func(c map[string]any) { c["iss"] = "other" }, expectedNonce: "nonce", secret: testSSOSecret},
		{name: "audience", mutate: func(c map[string]any) { c["aud"] = "other" }, expectedNonce: "nonce", secret: testSSOSecret},
		{name: "role", mutate: func(c map[string]any) { c["role"] = 9 }, expectedNonce: "nonce", secret: testSSOSecret},
		{name: "subject", mutate: func(c map[string]any) { delete(c, "sub") }, expectedNonce: "nonce", secret: testSSOSecret},
		{name: "nonce", mutate: func(c map[string]any) {}, expectedNonce: "different", secret: testSSOSecret},
		{name: "missing jti", mutate: func(c map[string]any) { delete(c, "jti") }, expectedNonce: "nonce", secret: testSSOSecret},
		{name: "expired", mutate: func(c map[string]any) {
			c["iat"] = now.Add(-2 * time.Minute).Unix()
			c["exp"] = now.Add(-time.Minute).Unix()
		}, expectedNonce: "nonce", secret: testSSOSecret},
		{name: "future", mutate: func(c map[string]any) {
			c["iat"] = now.Add(time.Minute).Unix()
			c["exp"] = now.Add(2 * time.Minute).Unix()
		}, expectedNonce: "nonce", secret: testSSOSecret},
		{name: "long lifetime", mutate: func(c map[string]any) { c["exp"] = now.Add(3 * time.Minute).Unix() }, expectedNonce: "nonce", secret: testSSOSecret},
		{name: "signature", mutate: func(c map[string]any) {}, expectedNonce: "nonce", secret: "abcdef0123456789abcdef0123456789"},
	}
	for index, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			claims := valid(tc.name + "-" + time.Unix(int64(index), 0).String())
			tc.mutate(claims)
			assertion := signTestSSOAssertion(t, tc.secret, claims)
			if _, _, _, err := sso.Exchange(assertion, tc.expectedNonce); !errors.Is(err, ErrSSOInvalidAssertion) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestNewSSORequiresCompleteConfiguration(t *testing.T) {
	tokenIssuer, err := New("admin", "password", "admin-token-secret", time.Hour)
	if err != nil {
		t.Fatalf("New auth: %v", err)
	}
	for _, tc := range []struct {
		name         string
		secret       string
		issuer       string
		audience     string
		parentOrigin string
		tokenIssuer  *Service
	}{
		{name: "short secret", secret: "short", issuer: "toporeduce", audience: "upstream-ops", parentOrigin: "https://api.example.com", tokenIssuer: tokenIssuer},
		{name: "missing issuer", secret: testSSOSecret, audience: "upstream-ops", parentOrigin: "https://api.example.com", tokenIssuer: tokenIssuer},
		{name: "missing audience", secret: testSSOSecret, issuer: "toporeduce", parentOrigin: "https://api.example.com", tokenIssuer: tokenIssuer},
		{name: "origin path", secret: testSSOSecret, issuer: "toporeduce", audience: "upstream-ops", parentOrigin: "https://api.example.com/path", tokenIssuer: tokenIssuer},
		{name: "auth disabled", secret: testSSOSecret, issuer: "toporeduce", audience: "upstream-ops", parentOrigin: "https://api.example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewSSO(tc.secret, tc.issuer, tc.audience, tc.parentOrigin, tc.tokenIssuer); err == nil {
				t.Fatal("expected configuration error")
			}
		})
	}
}

func signTestSSOAssertion(t *testing.T, secret string, claims any) string {
	t.Helper()
	headerBytes, err := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	claimsBytes, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	header := base64.RawURLEncoding.EncodeToString(headerBytes)
	payload := base64.RawURLEncoding.EncodeToString(claimsBytes)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(header + "." + payload))
	return header + "." + payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
