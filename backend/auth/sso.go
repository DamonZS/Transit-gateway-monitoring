package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	DefaultSSOAudience      = "upstream-ops"
	maxSSOAssertionBytes    = 8 * 1024
	maxSSONonceBytes        = 512
	maxSSOIdentifierBytes   = 512
	maxSSOAssertionLifetime = time.Minute
	ssoClockSkew            = 30 * time.Second
)

var (
	ErrSSOInvalidAssertion = errors.New("invalid SSO assertion")
	ErrSSOReplay           = errors.New("SSO assertion already used")
)

type ssoHeader struct {
	Algorithm string `json:"alg"`
	Type      string `json:"typ,omitempty"`
}

type ssoAudience []string

func (a *ssoAudience) UnmarshalJSON(data []byte) error {
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		*a = ssoAudience{single}
		return nil
	}
	var multiple []string
	if err := json.Unmarshal(data, &multiple); err != nil {
		return err
	}
	*a = multiple
	return nil
}

func (a ssoAudience) contains(expected string) bool {
	for _, value := range a {
		if value == expected {
			return true
		}
	}
	return false
}

type ssoClaims struct {
	Issuer   string      `json:"iss"`
	Audience ssoAudience `json:"aud"`
	Subject  string      `json:"sub"`
	Role     int         `json:"role"`
	Nonce    string      `json:"nonce"`
	JTI      string      `json:"jti"`
	IssuedAt int64       `json:"iat"`
	Expires  int64       `json:"exp"`
}

// SSOPublicConfig is safe to expose to an unauthenticated frontend. It never
// contains the shared assertion secret.
type SSOPublicConfig struct {
	Enabled      bool   `json:"enabled"`
	Issuer       string `json:"issuer,omitempty"`
	Audience     string `json:"audience,omitempty"`
	ParentOrigin string `json:"parent_origin,omitempty"`
}

// SSOService verifies assertions from the trusted parent application and
// exchanges them for the same administrator token used by password login.
type SSOService struct {
	sharedSecret []byte
	issuer       string
	audience     string
	parentOrigin string
	tokenIssuer  *Service

	mu      sync.Mutex
	usedJTI map[string]int64
	now     func() time.Time
}

func NewSSO(sharedSecret, issuer, audience, parentOrigin string, tokenIssuer *Service) (*SSOService, error) {
	if tokenIssuer == nil {
		return nil, errors.New("SSO requires enabled administrator authentication")
	}
	sharedSecret = strings.TrimSpace(sharedSecret)
	if len([]byte(sharedSecret)) < 32 || len([]byte(sharedSecret)) > 4096 {
		return nil, errors.New("SSO shared secret must be between 32 and 4096 bytes")
	}
	issuer = strings.TrimSpace(issuer)
	if issuer == "" {
		return nil, errors.New("SSO issuer is required")
	}
	audience = strings.TrimSpace(audience)
	if audience == "" {
		return nil, errors.New("SSO audience is required")
	}
	normalizedOrigin, err := normalizeParentOrigin(parentOrigin)
	if err != nil {
		return nil, err
	}
	return &SSOService{
		sharedSecret: []byte(sharedSecret),
		issuer:       issuer,
		audience:     audience,
		parentOrigin: normalizedOrigin,
		tokenIssuer:  tokenIssuer,
		usedJTI:      make(map[string]int64),
		now:          time.Now,
	}, nil
}

func normalizeParentOrigin(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("SSO parent origin must be an http(s) origin without a path")
	}
	return u.Scheme + "://" + u.Host, nil
}

func (s *SSOService) PublicConfig() SSOPublicConfig {
	if s == nil {
		return SSOPublicConfig{Enabled: false}
	}
	return SSOPublicConfig{
		Enabled:      s != nil,
		Issuer:       s.issuer,
		Audience:     s.audience,
		ParentOrigin: s.parentOrigin,
	}
}

// VerifySharedSecret validates a server-to-server integration credential
// against the same secret used to trust Toporeduce SSO assertions.
func (s *SSOService) VerifySharedSecret(candidate string) bool {
	if s == nil {
		return false
	}
	return subtle.ConstantTimeCompare(s.sharedSecret, []byte(candidate)) == 1
}

// Exchange verifies a signed assertion and returns a regular UpstreamOps
// administrator token. expectedNonce must be generated and retained by the
// embedding client; it is compared to the signed nonce before the JTI is used.
func (s *SSOService) Exchange(assertion, expectedNonce string) (string, time.Time, string, error) {
	claims, err := s.verify(assertion, expectedNonce)
	if err != nil {
		return "", time.Time{}, "", err
	}
	if !s.consumeJTI(claims.JTI, claims.Expires) {
		return "", time.Time{}, "", ErrSSOReplay
	}
	token, expiresAt, err := s.tokenIssuer.IssueToken()
	if err != nil {
		return "", time.Time{}, "", fmt.Errorf("issue administrator token: %w", err)
	}
	return token, expiresAt, s.tokenIssuer.Username(), nil
}

func (s *SSOService) verify(assertion, expectedNonce string) (*ssoClaims, error) {
	if s == nil || s.tokenIssuer == nil {
		return nil, ErrSSOInvalidAssertion
	}
	if len(assertion) == 0 || len(assertion) > maxSSOAssertionBytes ||
		len(expectedNonce) == 0 || len(expectedNonce) > maxSSONonceBytes {
		return nil, ErrSSOInvalidAssertion
	}
	parts := strings.Split(assertion, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return nil, ErrSSOInvalidAssertion
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, ErrSSOInvalidAssertion
	}
	mac := hmac.New(sha256.New, s.sharedSecret)
	_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return nil, ErrSSOInvalidAssertion
	}

	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, ErrSSOInvalidAssertion
	}
	var header ssoHeader
	if err := json.Unmarshal(headerBytes, &header); err != nil || header.Algorithm != "HS256" ||
		(header.Type != "" && !strings.EqualFold(header.Type, "JWT")) {
		return nil, ErrSSOInvalidAssertion
	}

	claimsBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrSSOInvalidAssertion
	}
	var claims ssoClaims
	if err := json.Unmarshal(claimsBytes, &claims); err != nil {
		return nil, ErrSSOInvalidAssertion
	}
	if claims.Issuer != s.issuer || !claims.Audience.contains(s.audience) || claims.Subject == "" ||
		len(claims.Subject) > maxSSOIdentifierBytes || claims.Role < 10 {
		return nil, ErrSSOInvalidAssertion
	}
	if claims.Nonce == "" || len(claims.Nonce) > maxSSONonceBytes ||
		subtle.ConstantTimeCompare([]byte(claims.Nonce), []byte(expectedNonce)) != 1 {
		return nil, ErrSSOInvalidAssertion
	}
	if claims.JTI == "" || len(claims.JTI) > maxSSOIdentifierBytes {
		return nil, ErrSSOInvalidAssertion
	}

	issuedAt := time.Unix(claims.IssuedAt, 0)
	expiresAt := time.Unix(claims.Expires, 0)
	now := s.now()
	if claims.IssuedAt <= 0 || claims.Expires <= 0 || !expiresAt.After(issuedAt) ||
		expiresAt.Sub(issuedAt) > maxSSOAssertionLifetime ||
		issuedAt.After(now.Add(ssoClockSkew)) || now.After(expiresAt.Add(ssoClockSkew)) {
		return nil, ErrSSOInvalidAssertion
	}
	return &claims, nil
}

func (s *SSOService) consumeJTI(jti string, expires int64) bool {
	now := s.now().Unix()
	s.mu.Lock()
	defer s.mu.Unlock()
	for used, expiry := range s.usedJTI {
		if expiry+int64(ssoClockSkew/time.Second) < now {
			delete(s.usedJTI, used)
		}
	}
	if _, exists := s.usedJTI[jti]; exists {
		return false
	}
	s.usedJTI[jti] = expires
	return true
}
