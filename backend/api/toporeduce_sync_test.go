package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bejix/upstream-ops/backend/auth"
	"github.com/bejix/upstream-ops/backend/channel"
	"github.com/bejix/upstream-ops/backend/config"
	"github.com/bejix/upstream-ops/backend/crypto"
	"github.com/bejix/upstream-ops/backend/runtimeconfig"
	"github.com/bejix/upstream-ops/backend/storage"
	"github.com/gin-gonic/gin"
)

const toporeduceSyncTestSecret = "toporeduce-sync-shared-secret-1234567890"

func TestToporeduceChannelSyncRequiresSharedSecret(t *testing.T) {
	router, _ := newToporeduceSyncTestServer(t)

	for _, tc := range []struct {
		name   string
		header string
	}{
		{name: "missing"},
		{name: "wrong", header: "Bearer wrong-secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := performToporeduceSync(t, router, tc.header, `{"source":"toporeduce","channels":[]}`)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusUnauthorized, rec.Body.String())
			}
		})
	}
}

func TestToporeduceChannelSyncAcceptsEmptySnapshot(t *testing.T) {
	router, _ := newToporeduceSyncTestServer(t)
	rec := performToporeduceSync(t, router, "Bearer "+toporeduceSyncTestSecret, `{"source":"toporeduce","channels":[]}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var response struct {
		Data toporeduceChannelSyncSummary `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Data.Created != 0 || response.Data.Updated != 0 || response.Data.Disabled != 0 ||
		response.Data.Unchanged != 0 || response.Data.Failed != 0 || len(response.Data.Errors) != 0 {
		t.Fatalf("summary = %#v, want all zero", response.Data)
	}
}

func TestToporeduceChannelSyncRejectsMissingOrNullChannelsWithoutCalibration(t *testing.T) {
	router, _ := newToporeduceSyncTestServer(t)
	created := performToporeduceSync(t, router, "Bearer "+toporeduceSyncTestSecret, `{
		"source":"toporeduce",
		"channels":[{
			"external_id":"toporeduce:price-monitor-site:5",
			"name":"Guarded channel",
			"type":"newapi",
			"site_url":"https://guarded.example.com",
			"username":"guarded@example.com",
			"credential_mode":"password",
			"password":"guarded-password",
			"monitor_enabled":true,
			"local_channel_ids":[5]
		}]
	}`)
	if created.Code != http.StatusOK || decodeToporeduceSyncSummary(t, created).Created != 1 {
		t.Fatalf("create status = %d, body = %s", created.Code, created.Body.String())
	}

	for name, body := range map[string]string{
		"missing": `{"source":"toporeduce"}`,
		"null":    `{"source":"toporeduce","channels":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			rec := performToporeduceSync(t, router, "Bearer "+toporeduceSyncTestSecret, body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			channels := listToporeduceSyncTestChannels(t, router)
			if len(channels) != 1 || !channels[0].MonitorEnabled {
				t.Fatalf("malformed snapshot calibrated managed channels: %#v", channels)
			}
		})
	}
}

func TestToporeduceChannelSyncCreatesManagedChannelIdempotently(t *testing.T) {
	router, deps := newToporeduceSyncTestServer(t)
	body := `{
		"source":"toporeduce",
		"channels":[{
			"external_id":"toporeduce:price-monitor-site:7",
			"name":"Primary upstream",
			"type":"newapi",
			"site_url":"https://upstream.example.com",
			"username":"ops@example.com",
			"credential_mode":"password",
			"password":"same-password",
			"monitor_enabled":true,
			"local_channel_ids":[11,22]
		}]
	}`

	first := performToporeduceSync(t, router, "Bearer "+toporeduceSyncTestSecret, body)
	if first.Code != http.StatusOK {
		t.Fatalf("first sync status = %d, body = %s", first.Code, first.Body.String())
	}
	firstSummary := decodeToporeduceSyncSummary(t, first)
	if firstSummary.Created != 1 || firstSummary.Updated != 0 || firstSummary.Unchanged != 0 || firstSummary.Failed != 0 {
		t.Fatalf("first summary = %#v", firstSummary)
	}

	channels := listToporeduceSyncTestChannels(t, router)
	if len(channels) != 1 {
		t.Fatalf("channel count = %d, want 1", len(channels))
	}
	created := channels[0]
	if created.Name != "Primary upstream" || created.Type != "newapi" || created.SiteURL != "https://upstream.example.com" ||
		created.ManagedSource != "toporeduce" || created.ManagedExternalID != "toporeduce:price-monitor-site:7" {
		t.Fatalf("created channel = %#v", created)
	}
	if len(created.ManagedLocalChannelIDs) != 2 || created.ManagedLocalChannelIDs[0] != 11 || created.ManagedLocalChannelIDs[1] != 22 {
		t.Fatalf("managed local channel ids = %#v", created.ManagedLocalChannelIDs)
	}

	if err := deps.Sessions.Upsert(&storage.AuthSession{ChannelID: created.ID, UserID: "88"}); err != nil {
		t.Fatalf("seed auth session: %v", err)
	}
	second := performToporeduceSync(t, router, "Bearer "+toporeduceSyncTestSecret, body)
	if second.Code != http.StatusOK {
		t.Fatalf("second sync status = %d, body = %s", second.Code, second.Body.String())
	}
	secondSummary := decodeToporeduceSyncSummary(t, second)
	if secondSummary.Created != 0 || secondSummary.Updated != 0 || secondSummary.Unchanged != 1 || secondSummary.Failed != 0 {
		t.Fatalf("second summary = %#v", secondSummary)
	}
	channels = listToporeduceSyncTestChannels(t, router)
	if len(channels) != 1 || channels[0].UserID != "88" {
		t.Fatalf("unchanged credential cleared session: %#v", channels)
	}

	updatedBody := `{
		"source":"toporeduce",
		"channels":[{
			"external_id":"toporeduce:price-monitor-site:7",
			"name":"Primary upstream renamed",
			"type":"newapi",
			"site_url":"https://new-upstream.example.com/",
			"username":"new-ops@example.com",
			"sort_order":5,
			"credential_mode":"password",
			"password":"changed-password",
			"monitor_enabled":true,
			"local_channel_ids":[33,22]
		}]
	}`
	updatedRec := performToporeduceSync(t, router, "Bearer "+toporeduceSyncTestSecret, updatedBody)
	if updatedRec.Code != http.StatusOK {
		t.Fatalf("updated sync status = %d, body = %s", updatedRec.Code, updatedRec.Body.String())
	}
	updatedSummary := decodeToporeduceSyncSummary(t, updatedRec)
	if updatedSummary.Updated != 1 || updatedSummary.Created != 0 || updatedSummary.Failed != 0 {
		t.Fatalf("updated summary = %#v", updatedSummary)
	}
	channels = listToporeduceSyncTestChannels(t, router)
	if len(channels) != 1 || channels[0].Name != "Primary upstream renamed" ||
		channels[0].SiteURL != "https://new-upstream.example.com" || channels[0].UserID != "" {
		t.Fatalf("updated channel = %#v", channels)
	}
	if len(channels[0].ManagedLocalChannelIDs) != 2 || channels[0].ManagedLocalChannelIDs[0] != 22 ||
		channels[0].ManagedLocalChannelIDs[1] != 33 {
		t.Fatalf("updated local channel ids = %#v", channels[0].ManagedLocalChannelIDs)
	}
}

func TestToporeduceChannelSyncDoesNotTakeOverManualChannelWithSameName(t *testing.T) {
	router, _ := newToporeduceSyncTestServer(t)
	token := toporeduceSyncTestAdminToken(t, router)
	manual := performToporeduceSyncAdminRequest(t, router, token, http.MethodPost, "/api/channels", `{
		"name":"Existing manual",
		"type":"newapi",
		"site_url":"https://manual.example.com",
		"username":"manual@example.com",
		"password":"manual-password",
		"credential_mode":"password",
		"monitor_enabled":true
	}`)
	if manual.Code != http.StatusOK {
		t.Fatalf("manual create status = %d, body = %s", manual.Code, manual.Body.String())
	}

	syncRec := performToporeduceSync(t, router, "Bearer "+toporeduceSyncTestSecret, `{
		"source":"toporeduce",
		"channels":[{
			"external_id":"toporeduce:price-monitor-site:9",
			"name":"Existing manual",
			"type":"newapi",
			"site_url":"https://managed.example.com",
			"username":"managed@example.com",
			"credential_mode":"password",
			"password":"managed-password",
			"monitor_enabled":true,
			"local_channel_ids":[99]
		}]
	}`)
	if syncRec.Code != http.StatusOK {
		t.Fatalf("sync status = %d, body = %s", syncRec.Code, syncRec.Body.String())
	}
	summary := decodeToporeduceSyncSummary(t, syncRec)
	if summary.Created != 1 || summary.Failed != 0 {
		t.Fatalf("summary = %#v", summary)
	}
	channels := listToporeduceSyncTestChannels(t, router)
	if len(channels) != 2 {
		t.Fatalf("channel count = %d, want 2", len(channels))
	}
	var manualChannel, managedChannel *toporeduceSyncTestChannel
	for i := range channels {
		if channels[i].ManagedSource == "" {
			manualChannel = &channels[i]
		} else {
			managedChannel = &channels[i]
		}
	}
	if manualChannel == nil || manualChannel.Name != "Existing manual" || manualChannel.SiteURL != "https://manual.example.com" {
		t.Fatalf("manual channel was changed or claimed: %#v", channels)
	}
	if managedChannel == nil || managedChannel.Name != "Existing manual [Toporeduce #9]" ||
		managedChannel.ManagedExternalID != "toporeduce:price-monitor-site:9" {
		t.Fatalf("managed collision channel = %#v", channels)
	}

	repeated := performToporeduceSync(t, router, "Bearer "+toporeduceSyncTestSecret, `{
		"source":"toporeduce",
		"channels":[{
			"external_id":"toporeduce:price-monitor-site:9",
			"name":"Existing manual",
			"type":"newapi",
			"site_url":"https://managed.example.com",
			"username":"managed@example.com",
			"credential_mode":"password",
			"password":"managed-password",
			"monitor_enabled":true,
			"local_channel_ids":[99]
		}]
	}`)
	repeatedSummary := decodeToporeduceSyncSummary(t, repeated)
	if repeated.Code != http.StatusOK || repeatedSummary.Unchanged != 1 || repeatedSummary.Created != 0 || repeatedSummary.Failed != 0 {
		t.Fatalf("repeated collision sync status = %d, summary = %#v", repeated.Code, repeatedSummary)
	}
	if channels = listToporeduceSyncTestChannels(t, router); len(channels) != 2 {
		t.Fatalf("repeated collision created duplicates: %#v", channels)
	}
}

func TestToporeduceChannelSyncControlUpdateKeepsExistingCredentialAndSession(t *testing.T) {
	router, deps := newToporeduceSyncTestServer(t)
	initial := `{
		"source":"toporeduce",
		"channels":[{
			"external_id":"toporeduce:price-monitor-site:15",
			"name":"Control update",
			"type":"newapi",
			"site_url":"https://control.example.com",
			"username":"control@example.com",
			"credential_mode":"password",
			"password":"original-password",
			"monitor_enabled":true,
			"local_channel_ids":[15]
		}]
	}`
	created := performToporeduceSync(t, router, "Bearer "+toporeduceSyncTestSecret, initial)
	if created.Code != http.StatusOK || decodeToporeduceSyncSummary(t, created).Created != 1 {
		t.Fatalf("create status = %d, body = %s", created.Code, created.Body.String())
	}
	channels := listToporeduceSyncTestChannels(t, router)
	if err := deps.Sessions.Upsert(&storage.AuthSession{ChannelID: channels[0].ID, UserID: "115"}); err != nil {
		t.Fatalf("seed auth session: %v", err)
	}

	controlOnly := `{
		"source":"toporeduce",
		"channels":[{
			"external_id":"toporeduce:price-monitor-site:15",
			"name":"Control update",
			"type":"newapi",
			"site_url":"https://control.example.com",
			"username":"control@example.com",
			"credential_mode":"password",
			"monitor_enabled":true,
			"local_channel_ids":[15,16]
		}]
	}`
	updated := performToporeduceSync(t, router, "Bearer "+toporeduceSyncTestSecret, controlOnly)
	if updated.Code != http.StatusOK {
		t.Fatalf("control update status = %d, body = %s", updated.Code, updated.Body.String())
	}
	summary := decodeToporeduceSyncSummary(t, updated)
	if summary.Updated != 1 || summary.Failed != 0 {
		t.Fatalf("control update summary = %#v", summary)
	}
	channels = listToporeduceSyncTestChannels(t, router)
	if len(channels) != 1 || channels[0].UserID != "115" || len(channels[0].ManagedLocalChannelIDs) != 2 {
		t.Fatalf("control update changed credential/session: %#v", channels)
	}
}

func TestToporeduceChannelSyncEndpointChangeInvalidatesSession(t *testing.T) {
	router, deps := newToporeduceSyncTestServer(t)
	initial := `{
		"source":"toporeduce",
		"channels":[{
			"external_id":"toporeduce:price-monitor-site:16",
			"name":"Endpoint update",
			"type":"newapi",
			"site_url":"https://old.example.com",
			"username":"old@example.com",
			"credential_mode":"password",
			"password":"unchanged-password",
			"monitor_enabled":true,
			"local_channel_ids":[16]
		}]
	}`
	created := performToporeduceSync(t, router, "Bearer "+toporeduceSyncTestSecret, initial)
	if created.Code != http.StatusOK || decodeToporeduceSyncSummary(t, created).Created != 1 {
		t.Fatalf("create status = %d, body = %s", created.Code, created.Body.String())
	}
	channels := listToporeduceSyncTestChannels(t, router)
	if err := deps.Sessions.Upsert(&storage.AuthSession{ChannelID: channels[0].ID, UserID: "116"}); err != nil {
		t.Fatalf("seed auth session: %v", err)
	}

	updated := performToporeduceSync(t, router, "Bearer "+toporeduceSyncTestSecret, `{
		"source":"toporeduce",
		"channels":[{
			"external_id":"toporeduce:price-monitor-site:16",
			"name":"Endpoint update",
			"type":"newapi",
			"site_url":"https://new.example.com",
			"username":"new@example.com",
			"credential_mode":"password",
			"password":"unchanged-password",
			"monitor_enabled":true,
			"local_channel_ids":[16]
		}]
	}`)
	if updated.Code != http.StatusOK || decodeToporeduceSyncSummary(t, updated).Updated != 1 {
		t.Fatalf("update status = %d, body = %s", updated.Code, updated.Body.String())
	}
	session, err := deps.Sessions.FindByChannel(channels[0].ID)
	if err != nil {
		t.Fatalf("find auth session: %v", err)
	}
	if session != nil {
		t.Fatalf("stale auth session was retained: %#v", session)
	}
}

func TestToporeduceChannelSyncReturnsPerItemFailureSummary(t *testing.T) {
	router, _ := newToporeduceSyncTestServer(t)
	rec := performToporeduceSync(t, router, "Bearer "+toporeduceSyncTestSecret, `{
		"source":"toporeduce",
		"channels":[
			{
				"external_id":"toporeduce:price-monitor-site:31",
				"name":"Valid item",
				"type":"newapi",
				"site_url":"https://valid.example.com",
				"username":"valid@example.com",
				"credential_mode":"password",
				"password":"valid-password",
				"monitor_enabled":true,
				"local_channel_ids":[31]
			},
			{
				"external_id":"toporeduce:price-monitor-site:32",
				"name":"Invalid item",
				"type":"unsupported",
				"site_url":"https://invalid.example.com",
				"credential_mode":"password",
				"password":"invalid-password",
				"monitor_enabled":true,
				"local_channel_ids":[32]
			}
		]
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	summary := decodeToporeduceSyncSummary(t, rec)
	if summary.Created != 1 || summary.Failed != 1 || len(summary.Errors) != 1 ||
		summary.Errors[0].ExternalID != "toporeduce:price-monitor-site:32" {
		t.Fatalf("summary = %#v", summary)
	}
	if channels := listToporeduceSyncTestChannels(t, router); len(channels) != 1 || channels[0].Name != "Valid item" {
		t.Fatalf("valid item was not committed independently: %#v", channels)
	}
}

func TestToporeduceChannelSyncSourceFailurePreservesExistingWithoutBlockingBatch(t *testing.T) {
	router, _ := newToporeduceSyncTestServer(t)
	initial := performToporeduceSync(t, router, "Bearer "+toporeduceSyncTestSecret, `{
		"source":"toporeduce",
		"channels":[{
			"external_id":"toporeduce:price-monitor-site:33",
			"name":"Waiting for session",
			"type":"newapi",
			"site_url":"https://waiting.example.com",
			"username":"waiting@example.com",
			"credential_mode":"token",
			"token_credential":"{\"access_token\":\"old-token\",\"user_id\":\"33\"}",
			"monitor_enabled":true,
			"local_channel_ids":[33]
		}]
	}`)
	if initial.Code != http.StatusOK || decodeToporeduceSyncSummary(t, initial).Created != 1 {
		t.Fatalf("initial status = %d, body = %s", initial.Code, initial.Body.String())
	}

	rec := performToporeduceSync(t, router, "Bearer "+toporeduceSyncTestSecret, `{
		"source":"toporeduce",
		"channels":[
			{
				"external_id":"toporeduce:price-monitor-site:33",
				"sync_error":"two-factor monitor site requires a cached session token",
				"name":"Waiting for session",
				"type":"newapi",
				"site_url":"https://waiting.example.com",
				"monitor_enabled":false,
				"local_channel_ids":[33]
			},
			{
				"external_id":"toporeduce:price-monitor-site:34",
				"name":"Ready channel",
				"type":"newapi",
				"site_url":"https://ready.example.com",
				"username":"ready@example.com",
				"credential_mode":"password",
				"password":"ready-password",
				"monitor_enabled":true,
				"local_channel_ids":[34]
			}
		]
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	summary := decodeToporeduceSyncSummary(t, rec)
	if summary.Created != 1 || summary.Disabled != 0 || summary.Failed != 1 || len(summary.Errors) != 1 {
		t.Fatalf("summary = %#v", summary)
	}
	channels := listToporeduceSyncTestChannels(t, router)
	if len(channels) != 2 {
		t.Fatalf("channels = %#v", channels)
	}
	for _, item := range channels {
		if item.ManagedExternalID == "toporeduce:price-monitor-site:33" && !item.MonitorEnabled {
			t.Fatalf("source-failed managed channel was disabled: %#v", item)
		}
	}
}

func TestToporeduceChannelSyncDisablesMissingManagedChannelsWithoutDeletingHistory(t *testing.T) {
	router, deps := newToporeduceSyncTestServer(t)
	initial := `{
		"source":"toporeduce",
		"channels":[
			{
				"external_id":"toporeduce:price-monitor-site:21",
				"name":"Managed one",
				"type":"newapi",
				"site_url":"https://one.example.com",
				"username":"one@example.com",
				"credential_mode":"password",
				"password":"one-password",
				"monitor_enabled":true,
				"local_channel_ids":[1]
			},
			{
				"external_id":"toporeduce:price-monitor-site:22",
				"name":"Managed two",
				"type":"sub2api",
				"site_url":"https://two.example.com",
				"username":"two@example.com",
				"credential_mode":"password",
				"password":"two-password",
				"monitor_enabled":true,
				"local_channel_ids":[2]
			}
		]
	}`
	created := performToporeduceSync(t, router, "Bearer "+toporeduceSyncTestSecret, initial)
	if created.Code != http.StatusOK || decodeToporeduceSyncSummary(t, created).Created != 2 {
		t.Fatalf("initial sync status = %d, body = %s", created.Code, created.Body.String())
	}
	managed := listToporeduceSyncTestChannels(t, router)
	if len(managed) != 2 {
		t.Fatalf("managed channel count = %d", len(managed))
	}
	if _, err := deps.Rates.Upsert(&storage.RateSnapshot{
		ChannelID:  managed[0].ID,
		ModelName:  "gpt-history",
		Ratio:      1.25,
		LastSeenAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed rate history: %v", err)
	}

	token := toporeduceSyncTestAdminToken(t, router)
	manual := performToporeduceSyncAdminRequest(t, router, token, http.MethodPost, "/api/channels", `{
		"name":"Manual survivor",
		"type":"newapi",
		"site_url":"https://manual-survivor.example.com",
		"username":"manual@example.com",
		"password":"manual-password",
		"credential_mode":"password",
		"monitor_enabled":true
	}`)
	if manual.Code != http.StatusOK {
		t.Fatalf("manual create status = %d, body = %s", manual.Code, manual.Body.String())
	}

	empty := performToporeduceSync(t, router, "Bearer "+toporeduceSyncTestSecret, `{"source":"toporeduce","channels":[]}`)
	if empty.Code != http.StatusOK {
		t.Fatalf("empty sync status = %d, body = %s", empty.Code, empty.Body.String())
	}
	summary := decodeToporeduceSyncSummary(t, empty)
	if summary.Disabled != 2 || summary.Failed != 0 {
		t.Fatalf("empty summary = %#v", summary)
	}
	all := listToporeduceSyncTestChannels(t, router)
	if len(all) != 3 {
		t.Fatalf("channel count after calibration = %d, want 3", len(all))
	}
	for _, item := range all {
		if item.ManagedSource == "toporeduce" && item.MonitorEnabled {
			t.Fatalf("managed channel remained enabled: %#v", item)
		}
		if item.ManagedSource == "" && !item.MonitorEnabled {
			t.Fatalf("manual channel was disabled: %#v", item)
		}
	}

	rates := performToporeduceSyncAdminRequest(t, router, token, http.MethodGet, "/api/channels/"+
		fmt.Sprint(managed[0].ID)+"/rates", "")
	if rates.Code != http.StatusOK {
		t.Fatalf("rates status = %d, body = %s", rates.Code, rates.Body.String())
	}
	var ratesResponse struct {
		Data []storage.RateSnapshot `json:"data"`
	}
	if err := json.Unmarshal(rates.Body.Bytes(), &ratesResponse); err != nil {
		t.Fatalf("decode rates: %v", err)
	}
	if len(ratesResponse.Data) != 1 || ratesResponse.Data[0].ModelName != "gpt-history" {
		t.Fatalf("history was deleted: %#v", ratesResponse.Data)
	}

	restored := performToporeduceSync(t, router, "Bearer "+toporeduceSyncTestSecret, initial)
	if restored.Code != http.StatusOK {
		t.Fatalf("restore status = %d, body = %s", restored.Code, restored.Body.String())
	}
	restoredSummary := decodeToporeduceSyncSummary(t, restored)
	if restoredSummary.Updated != 2 || restoredSummary.Failed != 0 {
		t.Fatalf("restore summary = %#v", restoredSummary)
	}
	for _, item := range listToporeduceSyncTestChannels(t, router) {
		if item.ManagedSource == "toporeduce" && !item.MonitorEnabled {
			t.Fatalf("returning managed channel was not re-enabled: %#v", item)
		}
	}
}

func newToporeduceSyncTestServer(t *testing.T) (*gin.Engine, *Deps) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := openTestDB(t)
	cipher, err := crypto.NewCipher("toporeduce-sync-test-app-secret")
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	channels := storage.NewChannels(db)
	sessions := storage.NewAuthSessions(db)
	rates := storage.NewRates(db)
	channelSvc := channel.NewService(
		channels,
		sessions,
		storage.NewCaptchas(db),
		rates,
		storage.NewMonitorLogs(db),
		cipher,
	)
	adminAuth, err := auth.New("admin", "password", "admin-token-secret", time.Hour)
	if err != nil {
		t.Fatalf("new auth service: %v", err)
	}
	sso, err := auth.NewSSO(toporeduceSyncTestSecret, "toporeduce", "upstream-ops", "https://api.toporeduce.cn", adminAuth)
	if err != nil {
		t.Fatalf("new SSO service: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runtime := runtimeconfig.New(
		"", "toporeduce-sync-test-app-secret", logger,
		nil, channelSvc, nil, adminAuth, nil,
		config.ProxyConfig{}, config.UpstreamConfig{}, config.GatewayConfig{}, nil,
	)
	runtime.SetSSO(sso)
	deps := &Deps{
		DB:         db,
		Cipher:     cipher,
		Runtime:    runtime,
		Channels:   channels,
		Sessions:   sessions,
		Rates:      rates,
		ChannelSvc: channelSvc,
	}
	router := gin.New()
	Register(router, deps)
	return router, deps
}

func performToporeduceSync(t *testing.T, router http.Handler, authorization, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/integrations/toporeduce/channels/sync", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func decodeToporeduceSyncSummary(t *testing.T, rec *httptest.ResponseRecorder) toporeduceChannelSyncSummary {
	t.Helper()
	var response struct {
		Data toporeduceChannelSyncSummary `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode sync response: %v", err)
	}
	return response.Data
}

type toporeduceSyncTestChannel struct {
	ID                     uint   `json:"id"`
	Name                   string `json:"name"`
	Type                   string `json:"type"`
	SiteURL                string `json:"site_url"`
	UserID                 string `json:"user_id"`
	ManagedSource          string `json:"managed_source"`
	ManagedExternalID      string `json:"managed_external_id"`
	ManagedLocalChannelIDs []int  `json:"managed_local_channel_ids"`
	MonitorEnabled         bool   `json:"monitor_enabled"`
}

func listToporeduceSyncTestChannels(t *testing.T, router http.Handler) []toporeduceSyncTestChannel {
	t.Helper()
	token := toporeduceSyncTestAdminToken(t, router)
	request := httptest.NewRequest(http.MethodGet, "/api/channels", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, request)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Data []toporeduceSyncTestChannel `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode channel list: %v", err)
	}
	return response.Data
}

func toporeduceSyncTestAdminToken(t *testing.T, router http.Handler) string {
	t.Helper()
	login := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewBufferString(`{"username":"admin","password":"password"}`))
	login.Header.Set("Content-Type", "application/json")
	loginRec := httptest.NewRecorder()
	router.ServeHTTP(loginRec, login)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("login status = %d, body = %s", loginRec.Code, loginRec.Body.String())
	}
	var loginResponse struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(loginRec.Body.Bytes(), &loginResponse); err != nil {
		t.Fatalf("decode login: %v", err)
	}
	return loginResponse.Data.Token
}

func performToporeduceSyncAdminRequest(t *testing.T, router http.Handler, token, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	request.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, request)
	return rec
}
