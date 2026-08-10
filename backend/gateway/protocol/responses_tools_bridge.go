package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	responsesToolSearchProxyName = "tool_search"
	responsesChatToolNameMaxLen  = 64
)

var responsesCustomToolSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"input": map[string]any{"type": "string"},
	},
	"required": []any{"input"},
}

var responsesToolSearchSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"query": map[string]any{"type": "string"},
		"limit": map[string]any{"type": "integer"},
	},
	"required": []any{"query"},
}

// NamespacedToolName 保存被 Chat 函数名扁平化的 Responses namespace 子工具。
type NamespacedToolName struct {
	Namespace string
	Name      string
}

// ResponsesToolBridge 是单次 Responses→Chat/Anthropic 转换的工具还原信息。
type ResponsesToolBridge struct {
	CustomTools       map[string]bool
	NamespaceTools    map[string]NamespacedToolName
	ToolSearchEnabled bool
}

func (b ResponsesToolBridge) hasSpecialTools() bool {
	return len(b.CustomTools) > 0 || len(b.NamespaceTools) > 0 || b.ToolSearchEnabled
}

// HasSpecialTools reports whether the request needs a non-standard tool
// round-trip when lowered to the Chat Completions protocol.
func (b ResponsesToolBridge) HasSpecialTools() bool {
	return b.hasSpecialTools()
}

func (b ResponsesToolBridge) chatToolName(name, namespace string) string {
	if namespace != "" {
		return flattenResponsesNamespaceToolName(namespace, name)
	}
	return name
}

func (b ResponsesToolBridge) outputTool(name, callID, itemID, arguments string) map[string]any {
	if b.CustomTools[name] {
		return map[string]any{
			"type":    "custom_tool_call",
			"id":      itemID,
			"call_id": callID,
			"name":    name,
			"input":   responsesCustomToolInput(arguments),
			"status":  "completed",
		}
	}
	if b.ToolSearchEnabled && name == responsesToolSearchProxyName {
		return map[string]any{
			"type":      "tool_search_call",
			"id":        itemID,
			"call_id":   callID,
			"execution": "client",
			"arguments": responsesToolSearchArguments(arguments),
			"status":    "completed",
		}
	}
	if ns, ok := b.NamespaceTools[name]; ok {
		return map[string]any{
			"type":      "function_call",
			"id":        itemID,
			"call_id":   callID,
			"name":      ns.Name,
			"namespace": ns.Namespace,
			"arguments": responsesToolArguments(arguments),
			"status":    "completed",
		}
	}
	return map[string]any{
		"type":      "function_call",
		"id":        itemID,
		"call_id":   callID,
		"name":      name,
		"arguments": responsesToolArguments(arguments),
		"status":    "completed",
	}
}

func (b ResponsesToolBridge) streamToolKind(name string) string {
	if b.CustomTools[name] {
		return "custom"
	}
	if b.ToolSearchEnabled && name == responsesToolSearchProxyName {
		return "tool_search"
	}
	return "function"
}

func (b ResponsesToolBridge) streamToolName(name string) (string, string) {
	if ns, ok := b.NamespaceTools[name]; ok {
		return ns.Name, ns.Namespace
	}
	return name, ""
}

func (b ResponsesToolBridge) streamToolItem(name, itemID, callID, arguments, status string) map[string]any {
	kind := b.streamToolKind(name)
	itemName, namespace := b.streamToolName(name)
	item := map[string]any{"id": itemID, "call_id": callID, "status": status}
	switch kind {
	case "custom":
		item["type"] = "custom_tool_call"
		item["name"] = name
		item["input"] = responsesCustomToolInput(arguments)
	case "tool_search":
		item["type"] = "tool_search_call"
		item["execution"] = "client"
		item["arguments"] = responsesToolSearchArguments(arguments)
	default:
		item["type"] = "function_call"
		item["name"] = itemName
		if status == "in_progress" {
			item["arguments"] = arguments
		} else {
			item["arguments"] = responsesToolArguments(arguments)
		}
		if namespace != "" {
			item["namespace"] = namespace
		}
	}
	return item
}

func responsesToolArguments(arguments string) string {
	if strings.TrimSpace(arguments) == "" {
		return "{}"
	}
	return arguments
}

func responsesCustomToolInput(arguments string) string {
	arguments = strings.TrimSpace(arguments)
	if arguments == "" {
		return ""
	}
	var input map[string]any
	if json.Unmarshal([]byte(arguments), &input) == nil {
		if value, ok := input["input"].(string); ok {
			return value
		}
	}
	return arguments
}

func responsesToolSearchArguments(arguments string) any {
	arguments = strings.TrimSpace(arguments)
	if arguments == "" {
		return map[string]any{}
	}
	var value any
	if json.Unmarshal([]byte(arguments), &value) == nil {
		return value
	}
	return arguments
}

// BuildResponsesToolBridge 提取顶层及 additional_tools 中的客户端工具。
func BuildResponsesToolBridge(body []byte) (ResponsesToolBridge, error) {
	var request map[string]any
	if err := json.Unmarshal(body, &request); err != nil {
		return ResponsesToolBridge{}, fmt.Errorf("invalid responses request: %w", err)
	}
	return buildResponsesToolBridge(request)
}

func buildResponsesToolBridge(request map[string]any) (ResponsesToolBridge, error) {
	tools := responsesEffectiveTools(request)
	bridge := ResponsesToolBridge{}
	topLevel := make(map[string]bool)
	functionNames := make(map[string]bool)
	customNames := make(map[string]bool)
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		if tool == nil {
			continue
		}
		typ, name := strings.TrimSpace(stringFromAny(tool["type"])), strings.TrimSpace(stringFromAny(tool["name"]))
		if (typ == "function" || typ == "custom") && name != "" {
			topLevel[name] = true
		}
		if typ == "function" && name != "" {
			functionNames[name] = true
		}
		if typ == "custom" && name != "" {
			customNames[name] = true
		}
	}
	for name := range customNames {
		if functionNames[name] {
			return ResponsesToolBridge{}, fmt.Errorf("custom tool %q conflicts with a function tool of the same name", name)
		}
	}
	owners := make(map[string]NamespacedToolName)
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		if tool == nil {
			continue
		}
		typ, name := strings.TrimSpace(stringFromAny(tool["type"])), strings.TrimSpace(stringFromAny(tool["name"]))
		switch typ {
		case "custom":
			if name != "" {
				if bridge.CustomTools == nil {
					bridge.CustomTools = make(map[string]bool)
				}
				bridge.CustomTools[name] = true
			}
		case "tool_search":
			if topLevel[responsesToolSearchProxyName] {
				return ResponsesToolBridge{}, fmt.Errorf("built-in tool_search conflicts with declared tool %q", responsesToolSearchProxyName)
			}
			bridge.ToolSearchEnabled = true
		case "namespace":
			if name == "" {
				continue
			}
			children := responsesNamespaceChildren(tool)
			for _, rawChild := range children {
				child, _ := rawChild.(map[string]any)
				if child == nil || strings.TrimSpace(stringFromAny(child["type"])) != "function" {
					continue
				}
				childName := strings.TrimSpace(stringFromAny(child["name"]))
				if childName == "" {
					continue
				}
				flat := flattenResponsesNamespaceToolName(name, childName)
				if topLevel[flat] {
					return ResponsesToolBridge{}, fmt.Errorf("namespace tool %q/%q conflicts with top-level tool %q", name, childName, flat)
				}
				entry := NamespacedToolName{Namespace: name, Name: childName}
				if old, exists := owners[flat]; exists && old != entry {
					return ResponsesToolBridge{}, fmt.Errorf("namespace tools %q/%q and %q/%q both map to %q", old.Namespace, old.Name, name, childName, flat)
				}
				owners[flat] = entry
				if bridge.NamespaceTools == nil {
					bridge.NamespaceTools = make(map[string]NamespacedToolName)
				}
				bridge.NamespaceTools[flat] = entry
			}
		}
	}
	if bridge.ToolSearchEnabled {
		if _, ok := bridge.NamespaceTools[responsesToolSearchProxyName]; ok {
			return ResponsesToolBridge{}, fmt.Errorf("built-in tool_search conflicts with namespace tool %q", responsesToolSearchProxyName)
		}
	}
	return bridge, nil
}

func responsesEffectiveTools(request map[string]any) []any {
	var tools []any
	if topLevel, ok := request["tools"].([]any); ok {
		tools = append(tools, topLevel...)
	}
	if input, ok := request["input"].([]any); ok {
		for _, raw := range input {
			item, _ := raw.(map[string]any)
			if item == nil || stringFromAny(item["type"]) != "additional_tools" {
				continue
			}
			if extra, ok := item["tools"].([]any); ok {
				tools = append(tools, extra...)
			}
		}
	}
	return tools
}

func responsesNamespaceChildren(tool map[string]any) []any {
	if children, ok := tool["tools"].([]any); ok {
		return children
	}
	children, _ := tool["children"].([]any)
	return children
}

func flattenResponsesNamespaceToolName(namespace, name string) string {
	full := namespace + "__" + name
	if len(full) <= responsesChatToolNameMaxLen {
		return full
	}
	sum := sha256.Sum256([]byte(full))
	suffix := "__" + hex.EncodeToString(sum[:4])
	limit := responsesChatToolNameMaxLen - len(suffix)
	var prefix strings.Builder
	for _, r := range full {
		if prefix.Len()+len(string(r)) > limit {
			break
		}
		prefix.WriteRune(r)
	}
	return prefix.String() + suffix
}

func stringFromAny(value any) string {
	text, _ := value.(string)
	return text
}
