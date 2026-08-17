package channel

import (
	"testing"

	"github.com/bejix/upstream-ops/backend/storage"
)

func TestAllowsManagedGroupScopesToporeduceChannel(t *testing.T) {
	source := "toporeduce"
	channel := &storage.Channel{ManagedSource: &source, ManagedGroups: []string{"GPT-Pro", "VIP"}}
	if !AllowsManagedGroup(channel, "GPT-Pro") {
		t.Fatal("assigned group was rejected")
	}
	if AllowsManagedGroup(channel, "GPT-Plus") {
		t.Fatal("unassigned group was accepted")
	}
	if AllowsManagedGroup(channel, "vip") {
		t.Fatal("group matching should remain case-sensitive")
	}
}

func TestAllowsManagedGroupKeepsLegacyManagedChannelCompatible(t *testing.T) {
	source := "toporeduce"
	channel := &storage.Channel{ManagedSource: &source}
	if !AllowsManagedGroup(channel, "legacy-group") {
		t.Fatal("legacy channel without group metadata should remain visible")
	}
}
