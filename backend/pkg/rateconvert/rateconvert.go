// Package rateconvert 统一上游分组倍率换算逻辑。
// 网关调度复用此算法，避免多处实现漂移。
package rateconvert

import "strings"

// Convert 按 mode 换算分组倍率。
//
// mode:
//   - raw / "" : 原值
//   - multiply_100 : v * 100
//   - divide_100 : v / 100
//   - custom : 使用 customValue
func Convert(v float64, mode string, customValue float64) float64 {
	switch strings.TrimSpace(mode) {
	case "multiply_100":
		return v * 100
	case "divide_100":
		return v / 100
	case "custom":
		return customValue
	default:
		return v
	}
}

// NormalizeMode 归一化 mode，空串视为 raw。
func NormalizeMode(mode string) string {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		return "raw"
	}
	return mode
}
