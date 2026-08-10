package gateway

import "testing"

func TestBillingInputWithResponsesImageOutput(t *testing.T) {
	input := billingInputFromRequest("/v1/responses", []byte(`{"tools":[{"type":"image_generation"}]}`))
	if input.EndpointMode != BillingModeImage {
		t.Fatalf("mode=%q", input.EndpointMode)
	}
	body := []byte(`{"type":"response.completed","response":{"output":[{"type":"image_generation_call","result":"image-1","size":"1024x1024"},{"type":"image_generation_call","result":"image-2"}],"tool_usage":{"image_gen":{"images":2}}}}`)
	input = billingInputWithResponse(input, body)
	if input.RequestCount != 2 || input.SizeTier != "1024x1024" {
		t.Fatalf("input=%+v", input)
	}
}

func TestParseOpenAIUsage_ResponsesTerminalImageUsage(t *testing.T) {
	body := []byte(`{"type":"response.completed","response":{"usage":{"input_tokens":10,"output_tokens":20},"tool_usage":{"image_gen":{"output_tokens_details":{"image_tokens":12}}}}}`)
	u := ParseOpenAIUsage(body)
	if u.InputTokens != 10 || u.OutputTokens != 20 || u.ImageOutputTokens != 12 {
		t.Fatalf("usage=%+v", u)
	}
}

func TestDefaultImageGenerationPricingMode(t *testing.T) {
	pricing := NewPricingCatalog(nil).Resolve("gemini-2.0-flash-exp-image-generation")
	if pricing.BillingMode != BillingModeImage || pricing.OutputPricePerImage != 0.034 {
		t.Fatalf("pricing=%+v", pricing)
	}
}

func TestResponsesWebSocketHelpers(t *testing.T) {
	url, err := responsesWebSocketURL("https://api.example.com/base/")
	if err != nil || url != "wss://api.example.com/base/v1/responses" {
		t.Fatalf("url=%q err=%v", url, err)
	}
	terminal, success, typ, msg := wsResponsesTerminal([]byte(`{"type":"response.failed","response":{"error":{"code":"server_error","message":"busy"}}}`))
	if !terminal || success || typ != "server_error" || msg != "busy" {
		t.Fatalf("terminal=%v success=%v type=%q message=%q", terminal, success, typ, msg)
	}
}
