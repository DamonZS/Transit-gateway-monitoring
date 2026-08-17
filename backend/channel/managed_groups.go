package channel

import (
	"strings"

	"github.com/bejix/upstream-ops/backend/storage"
)

const managedSourceToporeduce = "toporeduce"

// AllowsManagedGroup limits a synchronized Toporeduce channel to the groups
// assigned to that local channel. Unmanaged channels retain the existing
// behavior and allow all upstream groups.
func AllowsManagedGroup(c *storage.Channel, group string) bool {
	if c == nil || c.ManagedSource == nil || !strings.EqualFold(strings.TrimSpace(*c.ManagedSource), managedSourceToporeduce) {
		return true
	}
	// Records created by older sync payloads have no group metadata yet; keep
	// their historical all-groups behavior until the next enriched sync.
	if c.ManagedGroups == nil {
		return true
	}
	group = strings.TrimSpace(group)
	for _, allowed := range c.ManagedGroups {
		if strings.TrimSpace(allowed) == group {
			return true
		}
	}
	return false
}
