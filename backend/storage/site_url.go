package storage

import (
	"net/url"
	"strings"
)

// NormalizeSiteURL returns a stable identity for grouping records from the same upstream site.
func NormalizeSiteURL(value string) string {
	normalized := strings.TrimRight(strings.TrimSpace(value), "/")
	parsed, err := url.Parse(normalized)
	if err != nil || parsed.Host == "" {
		return normalized
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	return strings.TrimRight(parsed.String(), "/")
}
