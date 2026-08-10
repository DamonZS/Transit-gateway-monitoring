package gateway

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/bejix/upstream-ops/backend/gateway/protocol"
	"github.com/bejix/upstream-ops/backend/storage"
)

func TestApplyServiceTierRules(t *testing.T) {
	tests := []struct {
		name     string
		rules    string
		keyID    uint
		model    string
		kind     protocol.Kind
		body     string
		want     bool
		blocked  bool
		wantTier string
	}{
		{
			name:  "全局规则过滤全部 tier",
			rules: `[{"tier":"all","action":"filter"}]`,
			model: "gpt-5.5",
			kind:  protocol.KindOpenAIChat,
			body:  `{"model":"gpt-5.5","service_tier":"flex"}`,
			want:  false,
		},
		{
			name:  "指定多个 Key 规则优先于全部 Key 透传",
			rules: `[{"tier":"priority","action":"passthrough"},{"tier":"priority","action":"filter","key_ids":[41,42]}]`,
			keyID: 42,
			model: "gpt-5.5",
			kind:  protocol.KindOpenAIChat,
			body:  `{"model":"gpt-5.5","service_tier":"priority"}`,
			want:  false,
		},
		{
			name:  "未选中的 Key 回退到全部 Key 规则",
			rules: `[{"tier":"priority","action":"passthrough"},{"tier":"priority","action":"filter","key_ids":[41,42]}]`,
			keyID: 7,
			model: "gpt-5.5",
			kind:  protocol.KindOpenAIChat,
			body:  `{"model":"gpt-5.5","service_tier":"priority"}`,
			want:  true,
		},
		{
			name:  "指定 Key 留空时不匹配全部 Key",
			rules: `[{"tier":"priority","action":"filter","key_scope":"selected","key_ids":[]}]`,
			keyID: 7,
			model: "gpt-5.5",
			kind:  protocol.KindOpenAIChat,
			body:  `{"model":"gpt-5.5","service_tier":"priority"}`,
			want:  true,
		},
		{
			name:  "旧版指定用户规则继续生效",
			rules: `[{"tier":"priority","action":"filter","user_email":"alice@example.com"}]`,
			keyID: 7,
			model: "gpt-5.5",
			kind:  protocol.KindOpenAIChat,
			body:  `{"model":"gpt-5.5","service_tier":"priority"}`,
			want:  false,
		},
		{
			name:  "模型通配符命中",
			rules: `[{"tier":"flex","action":"filter","models":["gpt-5.5*"]}]`,
			model: "gpt-5.5-chat-latest",
			kind:  protocol.KindOpenAIResponses,
			body:  `{"model":"gpt-5.5-chat-latest","service_tier":"flex"}`,
			want:  false,
		},
		{
			name:  "模型未命中保持透传",
			rules: `[{"tier":"flex","action":"filter","models":["gpt-5.5*"]}]`,
			model: "gpt-4.1",
			kind:  protocol.KindOpenAIChat,
			body:  `{"model":"gpt-4.1","service_tier":"flex"}`,
			want:  true,
		},
		{
			name:     "透传时 fast 规范为 priority",
			rules:    `[{"tier":"priority","action":"passthrough"}]`,
			model:    "gpt-5.5",
			kind:     protocol.KindOpenAIChat,
			body:     `{"model":"gpt-5.5","service_tier":"fast"}`,
			want:     true,
			wantTier: "priority",
		},
		{
			name:     "强制 priority",
			rules:    `[{"tier":"flex","action":"force_priority"}]`,
			model:    "gpt-5.5",
			kind:     protocol.KindOpenAIChat,
			body:     `{"model":"gpt-5.5","service_tier":"flex"}`,
			want:     true,
			wantTier: "priority",
		},
		{
			name:    "阻断请求",
			rules:   `[{"tier":"priority","action":"block"}]`,
			model:   "gpt-5.5",
			kind:    protocol.KindOpenAIChat,
			body:    `{"model":"gpt-5.5","service_tier":"priority"}`,
			blocked: true,
		},
		{
			name:  "非 OpenAI 请求不处理",
			rules: `[{"tier":"all","action":"filter"}]`,
			model: "claude-sonnet",
			kind:  protocol.KindAnthropic,
			body:  `{"model":"claude-sonnet","service_tier":"flex"}`,
			want:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := &Runtime{}
			out, err := rt.applyServiceTierRules(
				&storage.GatewayGroup{ServiceTierRulesJSON: tt.rules},
				&storage.GatewayKey{ID: tt.keyID, OwnerEmail: "alice@example.com"},
				tt.kind,
				tt.model,
				[]byte(tt.body),
			)
			if tt.blocked {
				var blocked *serviceTierBlockedError
				if !errors.As(err, &blocked) {
					t.Fatalf("error = %v, want serviceTierBlockedError", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("apply rules: %v", err)
			}
			var got map[string]any
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("decode output: %v", err)
			}
			_, hasTier := got["service_tier"]
			if hasTier != tt.want {
				t.Fatalf("service_tier present = %v, want %v; body=%s", hasTier, tt.want, out)
			}
			if tt.wantTier != "" && got["service_tier"] != tt.wantTier {
				t.Fatalf("service_tier = %v, want %s", got["service_tier"], tt.wantTier)
			}
		})
	}
}

func TestNormalizeServiceTierRulesJSON(t *testing.T) {
	empty, err := normalizeServiceTierRulesJSON("")
	if err != nil {
		t.Fatalf("normalize empty rules: %v", err)
	}
	if empty != "" {
		t.Fatalf("empty rules = %q, want empty", empty)
	}

	raw, err := normalizeServiceTierRulesJSON(`[{"tier":"fast","action":"filter","key_ids":[41,42,41],"key_id":43,"models":[" gpt-5.5* ",""]}]`)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if raw != `[{"tier":"priority","action":"filter","key_scope":"selected","key_ids":[41,42,43],"models":["gpt-5.5*"]}]` {
		t.Fatalf("unexpected normalization: %s", raw)
	}
}
