package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bejix/upstream-ops/backend/auth"
	"github.com/bejix/upstream-ops/backend/config"
	"github.com/bejix/upstream-ops/backend/runtimeconfig"
	"github.com/gin-gonic/gin"
)

func TestSSOEndpointsArePublicAndIssueUsableToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const sharedSecret = "0123456789abcdef0123456789abcdef"
	authSvc, err := auth.New("admin", "password", "admin-token-secret", time.Hour)
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}
	ssoSvc, err := auth.NewSSO(sharedSecret, "toporeduce", "upstream-ops", "https://api.example.com", authSvc)
	if err != nil {
		t.Fatalf("auth.NewSSO: %v", err)
	}
	runtime := runtimeconfig.New(
		"", "", nil, nil, nil, nil, authSvc, nil,
		config.ProxyConfig{}, config.UpstreamConfig{}, config.GatewayConfig{}, nil,
	)
	runtime.SetSSO(ssoSvc)

	router := gin.New()
	router.Use(runtime.FrameAncestorsMiddleware())
	api := router.Group("/api")
	api.Use(runtime.AuthMiddleware())
	registerAuth(api, &Deps{Runtime: runtime})

	configRecorder := httptest.NewRecorder()
	router.ServeHTTP(configRecorder, httptest.NewRequest(http.MethodGet, "/api/auth/sso/config", nil))
	if configRecorder.Code != http.StatusOK || strings.Contains(configRecorder.Body.String(), sharedSecret) ||
		!strings.Contains(configRecorder.Body.String(), `"parent_origin":"https://api.example.com"`) {
		t.Fatalf("SSO config status=%d body=%s", configRecorder.Code, configRecorder.Body.String())
	}
	if got := configRecorder.Header().Get("Content-Security-Policy"); got != "frame-ancestors 'self' https://api.example.com" {
		t.Fatalf("Content-Security-Policy = %q", got)
	}

	now := time.Now()
	assertion := signAPIAssertion(t, sharedSecret, map[string]any{
		"iss": "toporeduce", "aud": "upstream-ops", "sub": "42", "role": 10,
		"nonce": "browser-nonce", "jti": "request-jti", "iat": now.Unix(), "exp": now.Add(time.Minute).Unix(),
	})
	body, _ := json.Marshal(map[string]string{"assertion": assertion, "nonce": "browser-nonce"})
	exchangeRecorder := httptest.NewRecorder()
	exchangeRequest := httptest.NewRequest(http.MethodPost, "/api/auth/sso/exchange", strings.NewReader(string(body)))
	exchangeRequest.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(exchangeRecorder, exchangeRequest)
	if exchangeRecorder.Code != http.StatusOK {
		t.Fatalf("exchange status=%d body=%s", exchangeRecorder.Code, exchangeRecorder.Body.String())
	}
	var response struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(exchangeRecorder.Body.Bytes(), &response); err != nil || response.Data.Token == "" {
		t.Fatalf("exchange response=%s err=%v", exchangeRecorder.Body.String(), err)
	}

	meRecorder := httptest.NewRecorder()
	meRequest := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	meRequest.Header.Set("Authorization", "Bearer "+response.Data.Token)
	router.ServeHTTP(meRecorder, meRequest)
	if meRecorder.Code != http.StatusOK || !strings.Contains(meRecorder.Body.String(), `"username":"admin"`) {
		t.Fatalf("me status=%d body=%s", meRecorder.Code, meRecorder.Body.String())
	}

	replayRecorder := httptest.NewRecorder()
	replayRequest := httptest.NewRequest(http.MethodPost, "/api/auth/sso/exchange", strings.NewReader(string(body)))
	replayRequest.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(replayRecorder, replayRequest)
	if replayRecorder.Code != http.StatusConflict {
		t.Fatalf("replay status=%d body=%s", replayRecorder.Code, replayRecorder.Body.String())
	}
}

func signAPIAssertion(t *testing.T, secret string, claims any) string {
	t.Helper()
	headerJSON, err := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	header := base64.RawURLEncoding.EncodeToString(headerJSON)
	payload := base64.RawURLEncoding.EncodeToString(claimsJSON)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(header + "." + payload))
	return header + "." + payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
