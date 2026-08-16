package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bejix/upstream-ops/backend/storage"
)

func TestToporeduceChannelMetricsRequiresSharedSecret(t *testing.T) {
	router, _ := newToporeduceSyncTestServer(t)

	for _, tc := range []struct {
		name       string
		header     string
		wantStatus int
	}{
		{name: "missing", wantStatus: http.StatusUnauthorized},
		{name: "wrong", header: "Bearer wrong-secret", wantStatus: http.StatusUnauthorized},
		{name: "shared secret", header: "Bearer " + toporeduceSyncTestSecret, wantStatus: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/integrations/toporeduce/channels/metrics", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}

func TestToporeduceChannelMetricsReturnsManagedStoredTelemetry(t *testing.T) {
	router, deps := newToporeduceSyncTestServer(t)
	now := time.Now().UTC().Truncate(time.Second)
	source := "toporeduce"
	externalID := "toporeduce:price-monitor-site:7"
	balance := 41.25
	todayCost := 3.5
	totalCost := 88.75
	balanceAt := now.Add(-2 * time.Minute)
	managed := storage.Channel{
		Name:                   "Managed upstream",
		ManagedSource:          &source,
		ManagedExternalID:      &externalID,
		ManagedLocalChannelIDs: []int{11, 22},
		Type:                   storage.ChannelTypeNewAPI,
		SiteURL:                "https://managed.example.com",
		Username:               "managed@example.com",
		PasswordCipher:         "ciphertext",
		MonitorEnabled:         true,
		LastBalance:            &balance,
		LastBalanceAt:          &balanceAt,
		TodayCost:              &todayCost,
		TotalCost:              &totalCost,
		LastError:              "rate refresh failed",
	}
	if err := deps.Channels.Create(&managed); err != nil {
		t.Fatalf("create managed channel: %v", err)
	}

	emptyExternalID := "toporeduce:price-monitor-site:8"
	emptyManaged := storage.Channel{
		Name:              "Managed without samples",
		ManagedSource:     &source,
		ManagedExternalID: &emptyExternalID,
		Type:              storage.ChannelTypeSub2API,
		SiteURL:           "https://empty.example.com",
		Username:          "empty@example.com",
		PasswordCipher:    "ciphertext",
		MonitorEnabled:    true,
	}
	if err := deps.Channels.Create(&emptyManaged); err != nil {
		t.Fatalf("create empty managed channel: %v", err)
	}

	manual := storage.Channel{
		Name:           "Manual channel",
		Type:           storage.ChannelTypeNewAPI,
		SiteURL:        "https://manual.example.com",
		Username:       "manual@example.com",
		PasswordCipher: "ciphertext",
		MonitorEnabled: true,
	}
	if err := deps.Channels.Create(&manual); err != nil {
		t.Fatalf("create manual channel: %v", err)
	}

	foreignSource := "another-integration"
	foreignExternalID := "another-integration:site:9"
	foreignManaged := storage.Channel{
		Name:              "Managed by another integration",
		ManagedSource:     &foreignSource,
		ManagedExternalID: &foreignExternalID,
		Type:              storage.ChannelTypeNewAPI,
		SiteURL:           "https://foreign.example.com",
		Username:          "foreign@example.com",
		PasswordCipher:    "ciphertext",
		MonitorEnabled:    true,
	}
	if err := deps.Channels.Create(&foreignManaged); err != nil {
		t.Fatalf("create foreign managed channel: %v", err)
	}

	groupID := int64(9)
	groupSeenAt := now.Add(-time.Minute)
	if _, err := deps.Rates.Upsert(&storage.RateSnapshot{
		ChannelID:       managed.ID,
		RemoteGroupID:   &groupID,
		ModelName:       "premium",
		Description:     "Premium group",
		Ratio:           0.05,
		CompletionRatio: 0.08,
		LastSeenAt:      groupSeenAt,
	}); err != nil {
		t.Fatalf("upsert rate snapshot: %v", err)
	}

	costSampledAt := now.Add(-90 * time.Second)
	if err := deps.Rates.AppendCost(&storage.CostSnapshot{
		ChannelID: managed.ID,
		TodayCost: todayCost,
		SampledAt: costSampledAt,
	}); err != nil {
		t.Fatalf("append cost snapshot: %v", err)
	}

	oldCheckStart := now.Add(-10 * time.Minute)
	if err := deps.MonLogs.Append(&storage.MonitorLog{
		ChannelID:  managed.ID,
		Job:        storage.MonitorJobBalance,
		Success:    true,
		StartedAt:  oldCheckStart,
		FinishedAt: oldCheckStart.Add(80 * time.Millisecond),
	}); err != nil {
		t.Fatalf("append old monitor log: %v", err)
	}
	latestCheckStart := now.Add(-time.Minute)
	if err := deps.MonLogs.Append(&storage.MonitorLog{
		ChannelID:    managed.ID,
		Job:          storage.MonitorJobRates,
		Success:      false,
		ErrorMessage: "rate refresh failed",
		StartedAt:    latestCheckStart,
		FinishedAt:   latestCheckStart.Add(250 * time.Millisecond),
	}); err != nil {
		t.Fatalf("append latest monitor log: %v", err)
	}

	usageRows := []storage.GatewayUsageLog{
		{RequestID: "recent-success", ChannelID: managed.ID, Success: true, DurationMS: 100, CreatedAt: now.Add(-time.Hour)},
		{RequestID: "recent-failure", ChannelID: managed.ID, Success: false, DurationMS: 300, CreatedAt: now.Add(-2 * time.Hour)},
		{RequestID: "outside-window", ChannelID: managed.ID, Success: true, DurationMS: 900, CreatedAt: now.Add(-25 * time.Hour)},
		{RequestID: "manual-channel", ChannelID: manual.ID, Success: true, DurationMS: 50, CreatedAt: now.Add(-time.Hour)},
	}
	for i := range usageRows {
		if err := deps.GatewayUsage.Create(&usageRows[i]); err != nil {
			t.Fatalf("create usage row %d: %v", i, err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/integrations/toporeduce/channels/metrics", nil)
	req.Header.Set("Authorization", "Bearer "+toporeduceSyncTestSecret)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var response struct {
		Data struct {
			GeneratedAt     time.Time `json:"generated_at"`
			WindowStartedAt time.Time `json:"window_started_at"`
			WindowEndedAt   time.Time `json:"window_ended_at"`
			Channels        []struct {
				ChannelID         uint       `json:"channel_id"`
				ManagedExternalID string     `json:"managed_external_id"`
				LocalChannelIDs   []int      `json:"local_channel_ids"`
				Name              string     `json:"name"`
				Type              string     `json:"type"`
				Balance           *float64   `json:"balance"`
				BalanceSampledAt  *time.Time `json:"balance_sampled_at"`
				TodayCost         *float64   `json:"today_cost"`
				TotalCost         *float64   `json:"total_cost"`
				CostSampledAt     *time.Time `json:"cost_sampled_at"`
				Health            struct {
					Status           string              `json:"status"`
					MonitorEnabled   bool                `json:"monitor_enabled"`
					LastError        string              `json:"last_error"`
					LatestMonitorLog *storage.MonitorLog `json:"latest_monitor_log"`
				} `json:"health"`
				UpstreamGroups []struct {
					RemoteGroupID   *int64    `json:"remote_group_id"`
					Name            string    `json:"name"`
					Description     string    `json:"description"`
					Ratio           float64   `json:"ratio"`
					CompletionRatio float64   `json:"completion_ratio"`
					FirstSeenAt     time.Time `json:"first_seen_at"`
					LastSeenAt      time.Time `json:"last_seen_at"`
				} `json:"upstream_groups"`
				Traffic24H struct {
					TotalRequests     int64    `json:"total_requests"`
					SuccessCount      int64    `json:"success_count"`
					ErrorCount        int64    `json:"error_count"`
					SuccessRate       *float64 `json:"success_rate"`
					AverageDurationMS *float64 `json:"average_duration_ms"`
				} `json:"traffic_24h"`
			} `json:"channels"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode metrics response: %v", err)
	}
	if response.Data.GeneratedAt.IsZero() || response.Data.WindowStartedAt.IsZero() || response.Data.WindowEndedAt.IsZero() {
		t.Fatalf("response timestamps are missing: %#v", response.Data)
	}
	if got := response.Data.WindowEndedAt.Sub(response.Data.WindowStartedAt); got != 24*time.Hour {
		t.Fatalf("traffic window = %s, want 24h", got)
	}
	if len(response.Data.Channels) != 2 {
		t.Fatalf("managed channels = %d, want 2; body = %s", len(response.Data.Channels), rec.Body.String())
	}

	var populated, empty *struct {
		ChannelID         uint       `json:"channel_id"`
		ManagedExternalID string     `json:"managed_external_id"`
		LocalChannelIDs   []int      `json:"local_channel_ids"`
		Name              string     `json:"name"`
		Type              string     `json:"type"`
		Balance           *float64   `json:"balance"`
		BalanceSampledAt  *time.Time `json:"balance_sampled_at"`
		TodayCost         *float64   `json:"today_cost"`
		TotalCost         *float64   `json:"total_cost"`
		CostSampledAt     *time.Time `json:"cost_sampled_at"`
		Health            struct {
			Status           string              `json:"status"`
			MonitorEnabled   bool                `json:"monitor_enabled"`
			LastError        string              `json:"last_error"`
			LatestMonitorLog *storage.MonitorLog `json:"latest_monitor_log"`
		} `json:"health"`
		UpstreamGroups []struct {
			RemoteGroupID   *int64    `json:"remote_group_id"`
			Name            string    `json:"name"`
			Description     string    `json:"description"`
			Ratio           float64   `json:"ratio"`
			CompletionRatio float64   `json:"completion_ratio"`
			FirstSeenAt     time.Time `json:"first_seen_at"`
			LastSeenAt      time.Time `json:"last_seen_at"`
		} `json:"upstream_groups"`
		Traffic24H struct {
			TotalRequests     int64    `json:"total_requests"`
			SuccessCount      int64    `json:"success_count"`
			ErrorCount        int64    `json:"error_count"`
			SuccessRate       *float64 `json:"success_rate"`
			AverageDurationMS *float64 `json:"average_duration_ms"`
		} `json:"traffic_24h"`
	}
	for i := range response.Data.Channels {
		channel := &response.Data.Channels[i]
		switch channel.ManagedExternalID {
		case externalID:
			populated = channel
		case emptyExternalID:
			empty = channel
		}
	}
	if populated == nil || empty == nil {
		t.Fatalf("missing managed channel in response: %s", rec.Body.String())
	}
	if populated.ChannelID != managed.ID || populated.Name != managed.Name || populated.Type != string(managed.Type) {
		t.Fatalf("managed identity = %#v", populated)
	}
	if len(populated.LocalChannelIDs) != 2 || populated.LocalChannelIDs[0] != 11 || populated.LocalChannelIDs[1] != 22 {
		t.Fatalf("local channel ids = %#v", populated.LocalChannelIDs)
	}
	if populated.Balance == nil || *populated.Balance != balance || populated.BalanceSampledAt == nil || !populated.BalanceSampledAt.Equal(balanceAt) {
		t.Fatalf("balance fields = value %#v sampled_at %#v", populated.Balance, populated.BalanceSampledAt)
	}
	if populated.TodayCost == nil || *populated.TodayCost != todayCost || populated.TotalCost == nil || *populated.TotalCost != totalCost ||
		populated.CostSampledAt == nil || !populated.CostSampledAt.Equal(costSampledAt) {
		t.Fatalf("cost fields = today %#v total %#v sampled_at %#v", populated.TodayCost, populated.TotalCost, populated.CostSampledAt)
	}
	if populated.Health.Status != "failed" || !populated.Health.MonitorEnabled || populated.Health.LastError != managed.LastError {
		t.Fatalf("health = %#v", populated.Health)
	}
	if populated.Health.LatestMonitorLog == nil || populated.Health.LatestMonitorLog.Job != storage.MonitorJobRates ||
		populated.Health.LatestMonitorLog.Success || populated.Health.LatestMonitorLog.DurationMS != 250 {
		t.Fatalf("latest monitor log = %#v", populated.Health.LatestMonitorLog)
	}
	if len(populated.UpstreamGroups) != 1 || populated.UpstreamGroups[0].RemoteGroupID == nil ||
		*populated.UpstreamGroups[0].RemoteGroupID != groupID || populated.UpstreamGroups[0].Name != "premium" ||
		populated.UpstreamGroups[0].Ratio != 0.05 || populated.UpstreamGroups[0].CompletionRatio != 0.08 ||
		!populated.UpstreamGroups[0].LastSeenAt.Equal(groupSeenAt) {
		t.Fatalf("upstream groups = %#v", populated.UpstreamGroups)
	}
	if populated.Traffic24H.TotalRequests != 2 || populated.Traffic24H.SuccessCount != 1 || populated.Traffic24H.ErrorCount != 1 ||
		populated.Traffic24H.SuccessRate == nil || *populated.Traffic24H.SuccessRate != 0.5 ||
		populated.Traffic24H.AverageDurationMS == nil || *populated.Traffic24H.AverageDurationMS != 200 {
		t.Fatalf("traffic_24h = %#v", populated.Traffic24H)
	}

	if empty.Balance != nil || empty.BalanceSampledAt != nil || empty.TodayCost != nil || empty.TotalCost != nil || empty.CostSampledAt != nil {
		t.Fatalf("empty financial samples must stay null: %#v", empty)
	}
	if empty.Health.LatestMonitorLog != nil {
		t.Fatalf("empty latest monitor log must stay null: %#v", empty.Health.LatestMonitorLog)
	}
	if len(empty.UpstreamGroups) != 0 {
		t.Fatalf("empty upstream groups = %#v", empty.UpstreamGroups)
	}
	if empty.Traffic24H.TotalRequests != 0 || empty.Traffic24H.SuccessRate != nil || empty.Traffic24H.AverageDurationMS != nil {
		t.Fatalf("empty traffic samples must preserve null rates: %#v", empty.Traffic24H)
	}
}
