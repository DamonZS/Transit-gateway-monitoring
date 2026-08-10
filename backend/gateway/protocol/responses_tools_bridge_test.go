package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestResponsesToolBridge_RoundTripClientTools(t *testing.T) {
	request := []byte(`{
        "model":"gpt-x",
        "input":[
          {"type":"custom_tool_call","call_id":"custom_1","name":"exec","input":"pwd"},
          {"type":"tool_search_call","call_id":"search_1","arguments":{"query":"git"}},
          {"type":"function_call","call_id":"read_1","namespace":"workspace","name":"read","arguments":"{\"path\":\"a\"}"}
        ],
        "tools":[
          {"type":"custom","name":"exec","description":"run"},
          {"type":"tool_search"},
          {"type":"namespace","name":"workspace","tools":[{"type":"function","name":"read","parameters":{"type":"object"}}]}
        ]
    }`)
	chat, bridge, err := ResponsesToOpenAIChatRequestWithToolBridge(request, "gpt-x", false)
	if err != nil {
		t.Fatal(err)
	}
	if !bridge.CustomTools["exec"] || !bridge.ToolSearchEnabled || bridge.NamespaceTools["workspace__read"].Name != "read" {
		t.Fatalf("bridge=%+v", bridge)
	}
	var lowered map[string]any
	if err := json.Unmarshal(chat, &lowered); err != nil {
		t.Fatal(err)
	}
	encoded := string(chat)
	for _, name := range []string{`"name":"exec"`, `"name":"tool_search"`, `"name":"workspace__read"`} {
		if !strings.Contains(encoded, name) {
			t.Fatalf("lowered tools missing %s: %s", name, encoded)
		}
	}

	response := []byte(`{
      "id":"chat_1","model":"gpt-x","choices":[{"message":{"role":"assistant","content":null,"tool_calls":[
        {"id":"custom_2","type":"function","function":{"name":"exec","arguments":"{\"input\":\"ls\"}"}},
        {"id":"search_2","type":"function","function":{"name":"tool_search","arguments":"{\"query\":\"files\"}"}},
        {"id":"read_2","type":"function","function":{"name":"workspace__read","arguments":"{\"path\":\"b\"}"}}
      ]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":3,"completion_tokens":2}
    }`)
	responses, err := OpenAIChatToResponsesResponseWithToolBridge(response, "gpt-x", bridge)
	if err != nil {
		t.Fatal(err)
	}
	text := string(responses)
	for _, fragment := range []string{`"type":"custom_tool_call"`, `"input":"ls"`, `"type":"tool_search_call"`, `"execution":"client"`, `"namespace":"workspace"`, `"name":"read"`} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("restored response missing %s: %s", fragment, text)
		}
	}
}

func TestChatToResponsesStreamWithToolBridge(t *testing.T) {
	bridge := ResponsesToolBridge{
		CustomTools:       map[string]bool{"exec": true},
		ToolSearchEnabled: true,
		NamespaceTools:    map[string]NamespacedToolName{"workspace__read": {Namespace: "workspace", Name: "read"}},
	}
	stream := NewChatToResponsesStreamWithToolBridge("gpt-x", bridge)
	var out strings.Builder
	for _, frame := range stream.FeedData(`{"id":"chat_1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"exec","arguments":"{\"input\":\"pwd\"}"}},{"index":1,"id":"c2","function":{"name":"tool_search","arguments":"{\"query\":\"git\"}"}},{"index":2,"id":"c3","function":{"name":"workspace__read","arguments":"{\"path\":\"a\"}"}}]},"finish_reason":"tool_calls"}]}`) {
		out.Write(frame)
	}
	for _, frame := range stream.Close() {
		out.Write(frame)
	}
	text := out.String()
	for _, fragment := range []string{"response.custom_tool_call_input.done", `"type":"custom_tool_call"`, `"type":"tool_search_call"`, `"execution":"client"`, `"namespace":"workspace"`, "response.completed"} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("stream missing %s: %s", fragment, text)
		}
	}
}
