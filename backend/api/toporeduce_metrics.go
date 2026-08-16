package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/bejix/upstream-ops/backend/storage"
	"github.com/gin-gonic/gin"
)

const toporeduceTrafficWindow = 24 * time.Hour

type toporeduceChannelMetricsData struct {
	GeneratedAt     time.Time                      `json:"generated_at"`
	WindowStartedAt time.Time                      `json:"window_started_at"`
	WindowEndedAt   time.Time                      `json:"window_ended_at"`
	Channels        []toporeduceChannelMetricsItem `json:"channels"`
}

type toporeduceChannelMetricsItem struct {
	ChannelID         uint                        `json:"channel_id"`
	ManagedExternalID *string                     `json:"managed_external_id"`
	LocalChannelIDs   []int                       `json:"local_channel_ids"`
	Name              string                      `json:"name"`
	Type              string                      `json:"type"`
	Balance           *float64                    `json:"balance"`
	BalanceSampledAt  *time.Time                  `json:"balance_sampled_at"`
	TodayCost         *float64                    `json:"today_cost"`
	TotalCost         *float64                    `json:"total_cost"`
	CostSampledAt     *time.Time                  `json:"cost_sampled_at"`
	Health            toporeduceChannelHealth     `json:"health"`
	UpstreamGroups    []toporeduceUpstreamGroup   `json:"upstream_groups"`
	Traffic24H        toporeduceChannelTraffic24H `json:"traffic_24h"`
}

type toporeduceChannelHealth struct {
	Status           string              `json:"status"`
	MonitorEnabled   bool                `json:"monitor_enabled"`
	LastError        string              `json:"last_error"`
	LatestMonitorLog *storage.MonitorLog `json:"latest_monitor_log"`
}

type toporeduceUpstreamGroup struct {
	RemoteGroupID   *int64    `json:"remote_group_id"`
	Name            string    `json:"name"`
	Description     string    `json:"description"`
	Ratio           float64   `json:"ratio"`
	CompletionRatio float64   `json:"completion_ratio"`
	FirstSeenAt     time.Time `json:"first_seen_at"`
	LastSeenAt      time.Time `json:"last_seen_at"`
}

type toporeduceChannelTraffic24H struct {
	TotalRequests     int64    `json:"total_requests"`
	SuccessCount      int64    `json:"success_count"`
	ErrorCount        int64    `json:"error_count"`
	SuccessRate       *float64 `json:"success_rate"`
	AverageDurationMS *float64 `json:"average_duration_ms"`
}

func getToporeduceChannelMetrics(c *gin.Context, d *Deps) {
	if !authorizeToporeduceIntegration(c, d) {
		return
	}
	if d.Channels == nil || d.Rates == nil || d.MonLogs == nil || d.GatewayUsage == nil {
		fail(c, http.StatusInternalServerError, errors.New("Toporeduce metrics dependencies are unavailable"))
		return
	}

	windowEnd := time.Now().UTC()
	windowStart := windowEnd.Add(-toporeduceTrafficWindow)
	channels, err := d.Channels.ListManaged("toporeduce")
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}

	items := make([]toporeduceChannelMetricsItem, 0, len(channels))
	for i := range channels {
		item, err := buildToporeduceChannelMetrics(d, channels[i], windowStart, windowEnd)
		if err != nil {
			fail(c, http.StatusInternalServerError, err)
			return
		}
		items = append(items, item)
	}

	c.JSON(http.StatusOK, gin.H{"data": toporeduceChannelMetricsData{
		GeneratedAt:     windowEnd,
		WindowStartedAt: windowStart,
		WindowEndedAt:   windowEnd,
		Channels:        items,
	}})
}

func buildToporeduceChannelMetrics(d *Deps, channel storage.Channel, windowStart, windowEnd time.Time) (toporeduceChannelMetricsItem, error) {
	rates, err := d.Rates.ListByChannel(channel.ID)
	if err != nil {
		return toporeduceChannelMetricsItem{}, err
	}
	groups := make([]toporeduceUpstreamGroup, 0, len(rates))
	for _, rate := range rates {
		groups = append(groups, toporeduceUpstreamGroup{
			RemoteGroupID:   rate.RemoteGroupID,
			Name:            rate.ModelName,
			Description:     rate.Description,
			Ratio:           rate.Ratio,
			CompletionRatio: rate.CompletionRatio,
			FirstSeenAt:     rate.FirstSeenAt,
			LastSeenAt:      rate.LastSeenAt,
		})
	}

	costHistory, err := d.Rates.CostHistory(channel.ID, 1)
	if err != nil {
		return toporeduceChannelMetricsItem{}, err
	}
	var costSampledAt *time.Time
	if len(costHistory) > 0 {
		sampledAt := costHistory[0].SampledAt
		costSampledAt = &sampledAt
	}

	monitorLogs, err := d.MonLogs.List(channel.ID, 1)
	if err != nil {
		return toporeduceChannelMetricsItem{}, err
	}
	var latestMonitorLog *storage.MonitorLog
	if len(monitorLogs) > 0 {
		log := monitorLogs[0]
		latestMonitorLog = &log
	}

	stats, err := d.GatewayUsage.Stats(storage.GatewayUsageQuery{
		ChannelID: channel.ID,
		From:      &windowStart,
		To:        &windowEnd,
	})
	if err != nil {
		return toporeduceChannelMetricsItem{}, err
	}
	traffic := toporeduceChannelTraffic24H{
		TotalRequests: stats.TotalRequests,
		SuccessCount:  stats.SuccessCount,
		ErrorCount:    stats.ErrorCount,
	}
	if stats.TotalRequests > 0 {
		successRate := float64(stats.SuccessCount) / float64(stats.TotalRequests)
		averageDurationMS := stats.AverageDurationMS
		traffic.SuccessRate = &successRate
		traffic.AverageDurationMS = &averageDurationMS
	}

	return toporeduceChannelMetricsItem{
		ChannelID:         channel.ID,
		ManagedExternalID: channel.ManagedExternalID,
		LocalChannelIDs:   append([]int{}, channel.ManagedLocalChannelIDs...),
		Name:              channel.Name,
		Type:              string(channel.Type),
		Balance:           channel.LastBalance,
		BalanceSampledAt:  channel.LastBalanceAt,
		TodayCost:         channel.TodayCost,
		TotalCost:         channel.TotalCost,
		CostSampledAt:     costSampledAt,
		Health: toporeduceChannelHealth{
			Status:           toporeduceChannelHealthStatus(channel),
			MonitorEnabled:   channel.MonitorEnabled,
			LastError:        channel.LastError,
			LatestMonitorLog: latestMonitorLog,
		},
		UpstreamGroups: groups,
		Traffic24H:     traffic,
	}, nil
}

func toporeduceChannelHealthStatus(channel storage.Channel) string {
	if channel.LastError != "" {
		return "failed"
	}
	if channel.MonitorEnabled {
		return "active"
	}
	return "disabled"
}
