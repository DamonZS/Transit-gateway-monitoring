package api

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/bejix/upstream-ops/backend/channel"
	"github.com/bejix/upstream-ops/backend/storage"
	"github.com/gin-gonic/gin"
)

type toporeduceChannelSyncRequest struct {
	Source   string                        `json:"source"`
	Channels *[]toporeduceChannelSyncInput `json:"channels"`
}

type toporeduceChannelSyncInput struct {
	ExternalID       string `json:"external_id"`
	SyncError        string `json:"sync_error"`
	Name             string `json:"name"`
	Type             string `json:"type"`
	SiteURL          string `json:"site_url"`
	Username         string `json:"username"`
	SortOrder        int    `json:"sort_order"`
	CredentialMode   string `json:"credential_mode"`
	Password         string `json:"password"`
	TokenCredential  string `json:"token_credential"`
	LoginExtraParams string `json:"login_extra_params"`
	MonitorEnabled   bool   `json:"monitor_enabled"`
	LocalChannelIDs  []int  `json:"local_channel_ids"`
}

type toporeduceChannelSyncError struct {
	ExternalID string `json:"external_id"`
	Message    string `json:"message"`
}

type toporeduceChannelSyncSummary struct {
	Created   int                          `json:"created"`
	Updated   int                          `json:"updated"`
	Disabled  int                          `json:"disabled"`
	Unchanged int                          `json:"unchanged"`
	Failed    int                          `json:"failed"`
	Errors    []toporeduceChannelSyncError `json:"errors,omitempty"`
}

func registerToporeduceIntegration(g *gin.RouterGroup, d *Deps) {
	g.POST("/integrations/toporeduce/channels/sync", func(c *gin.Context) {
		syncToporeduceChannels(c, d)
	})
	g.GET("/integrations/toporeduce/channels/metrics", func(c *gin.Context) {
		getToporeduceChannelMetrics(c, d)
	})
}

func syncToporeduceChannels(c *gin.Context, d *Deps) {
	if !authorizeToporeduceIntegration(c, d) {
		return
	}

	var request toporeduceChannelSyncRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	if request.Source != "toporeduce" {
		fail(c, http.StatusBadRequest, errors.New("source must be toporeduce"))
		return
	}
	if request.Channels == nil {
		fail(c, http.StatusBadRequest, errors.New("channels must be an array"))
		return
	}
	summary, err := applyToporeduceChannelSnapshot(d, request)
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": summary})
}

func authorizeToporeduceIntegration(c *gin.Context, d *Deps) bool {
	secret, ok := bearerCredential(c.GetHeader("Authorization"))
	if !ok || d == nil || d.Runtime == nil || !d.Runtime.VerifySSOSharedSecret(secret) {
		fail(c, http.StatusUnauthorized, errors.New("invalid Toporeduce integration credential"))
		return false
	}
	return true
}

func applyToporeduceChannelSnapshot(d *Deps, request toporeduceChannelSyncRequest) (toporeduceChannelSyncSummary, error) {
	summary := toporeduceChannelSyncSummary{}
	if d == nil || d.Channels == nil || d.ChannelSvc == nil || d.Cipher == nil {
		return summary, errors.New("channel sync dependencies are unavailable")
	}
	expected := make(map[string]struct{}, len(*request.Channels))
	for _, input := range *request.Channels {
		externalID := strings.TrimSpace(input.ExternalID)
		if externalID != "" {
			expected[externalID] = struct{}{}
		}
		if sourceError := strings.TrimSpace(input.SyncError); sourceError != "" {
			if externalID == "" {
				sourceError += "; external_id is required"
			}
			summary.Failed++
			summary.Errors = append(summary.Errors, toporeduceChannelSyncError{
				ExternalID: externalID,
				Message:    sourceError,
			})
			continue
		}
		outcome, err := upsertToporeduceChannel(d, request.Source, input)
		if err != nil {
			summary.Failed++
			summary.Errors = append(summary.Errors, toporeduceChannelSyncError{
				ExternalID: strings.TrimSpace(input.ExternalID),
				Message:    err.Error(),
			})
			continue
		}
		switch outcome {
		case "created":
			summary.Created++
		case "updated":
			summary.Updated++
		case "unchanged":
			summary.Unchanged++
		}
	}

	managed, err := d.Channels.ListManaged(request.Source)
	if err != nil {
		return summary, err
	}
	for _, existing := range managed {
		if existing.ManagedExternalID == nil {
			continue
		}
		if _, ok := expected[*existing.ManagedExternalID]; ok || !existing.MonitorEnabled {
			continue
		}
		disabled := false
		if _, err := d.ChannelSvc.Update(existing.ID, channel.UpdateInput{MonitorEnabled: &disabled}); err != nil {
			summary.Failed++
			summary.Errors = append(summary.Errors, toporeduceChannelSyncError{
				ExternalID: *existing.ManagedExternalID,
				Message:    "disable missing managed channel: " + err.Error(),
			})
			continue
		}
		summary.Disabled++
	}
	return summary, nil
}

func upsertToporeduceChannel(d *Deps, source string, input toporeduceChannelSyncInput) (string, error) {
	if d == nil || d.Channels == nil || d.ChannelSvc == nil || d.Cipher == nil {
		return "", errors.New("channel sync dependencies are unavailable")
	}
	externalID := strings.TrimSpace(input.ExternalID)
	name := strings.TrimSpace(input.Name)
	siteURL := strings.TrimRight(strings.TrimSpace(input.SiteURL), "/")
	if externalID == "" || name == "" || siteURL == "" {
		return "", errors.New("external_id, name and site_url are required")
	}
	channelType := storage.ChannelType(strings.ToLower(strings.TrimSpace(input.Type)))
	if channelType != storage.ChannelTypeNewAPI && channelType != storage.ChannelTypeSub2API {
		return "", fmt.Errorf("unsupported channel type %q", input.Type)
	}
	existing, err := d.Channels.FindManaged(source, externalID)
	if err != nil {
		return "", err
	}
	currentID := uint(0)
	if existing != nil {
		currentID = existing.ID
	}
	name, err = availableToporeduceManagedName(d.Channels, name, externalID, currentID)
	if err != nil {
		return "", err
	}
	mode := storage.CredentialMode(strings.ToLower(strings.TrimSpace(input.CredentialMode)))
	if mode == "" {
		if existing != nil && existing.CredentialMode != "" {
			mode = existing.CredentialMode
		} else {
			mode = storage.CredentialModePassword
		}
	}
	rawCredential, credentialProvided, err := toporeduceCredential(mode, input)
	if err != nil {
		return "", err
	}
	localChannelIDs := normalizeLocalChannelIDs(input.LocalChannelIDs)
	sortOrder := input.SortOrder
	if sortOrder == 0 {
		sortOrder = 1
	}

	if existing == nil {
		if !credentialProvided {
			return "", errors.New("credential is required when creating a managed channel")
		}
		_, err := d.ChannelSvc.Create(channel.CreateInput{
			Name:                   name,
			ManagedSource:          source,
			ManagedExternalID:      externalID,
			ManagedLocalChannelIDs: localChannelIDs,
			Type:                   channelType,
			SiteURL:                siteURL,
			Username:               strings.TrimSpace(input.Username),
			SortOrder:              sortOrder,
			Password:               input.Password,
			CredentialMode:         mode,
			TokenCredential:        input.TokenCredential,
			LoginExtraParams:       input.LoginExtraParams,
			MonitorEnabled:         input.MonitorEnabled,
		})
		if err != nil {
			return "", err
		}
		return "created", nil
	}

	update := channel.UpdateInput{}
	changed := false
	setString := func(current, desired string, target **string) {
		if current != desired {
			value := desired
			*target = &value
			changed = true
		}
	}
	setString(existing.Name, name, &update.Name)
	setString(existing.SiteURL, siteURL, &update.SiteURL)
	setString(existing.Username, strings.TrimSpace(input.Username), &update.Username)
	setString(existing.LoginExtraParams, strings.TrimSpace(input.LoginExtraParams), &update.LoginExtraParams)
	if existing.Type != channelType {
		value := channelType
		update.Type = &value
		changed = true
	}
	if existing.SortOrder != sortOrder {
		value := sortOrder
		update.SortOrder = &value
		changed = true
	}
	if existing.MonitorEnabled != input.MonitorEnabled {
		value := input.MonitorEnabled
		update.MonitorEnabled = &value
		changed = true
	}
	if !slices.Equal(existing.ManagedLocalChannelIDs, localChannelIDs) {
		value := append([]int(nil), localChannelIDs...)
		update.ManagedLocalChannelIDs = &value
		changed = true
	}
	existingMode := existing.CredentialMode
	if existingMode == "" {
		existingMode = storage.CredentialModePassword
	}
	credentialShapeChanged := existingMode != mode || existing.Type != channelType
	if credentialShapeChanged && !credentialProvided {
		return "", errors.New("credential is required when changing channel type or credential mode")
	}
	credentialChanged := credentialShapeChanged
	if credentialProvided {
		currentCredential, decryptErr := d.Cipher.Decrypt(existing.PasswordCipher)
		credentialChanged = credentialChanged || decryptErr != nil || currentCredential != rawCredential
	}
	if credentialChanged {
		value := mode
		update.CredentialMode = &value
		if mode == storage.CredentialModePassword {
			credential := input.Password
			update.Password = &credential
		} else {
			credential := input.TokenCredential
			update.TokenCredential = &credential
		}
		changed = true
	}
	if !changed {
		return "unchanged", nil
	}
	if _, err := d.ChannelSvc.Update(existing.ID, update); err != nil {
		return "", err
	}
	return "updated", nil
}

func toporeduceCredential(mode storage.CredentialMode, input toporeduceChannelSyncInput) (string, bool, error) {
	switch mode {
	case storage.CredentialModePassword:
		if input.Password == "" {
			return "", false, nil
		}
		return input.Password, true, nil
	case storage.CredentialModeToken:
		if input.TokenCredential == "" {
			return "", false, nil
		}
		return input.TokenCredential, true, nil
	default:
		return "", false, fmt.Errorf("unsupported credential mode %q", input.CredentialMode)
	}
}

func normalizeLocalChannelIDs(ids []int) []int {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[int]struct{}, len(ids))
	out := make([]int, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Ints(out)
	return out
}

func availableToporeduceManagedName(channels *storage.Channels, desired, externalID string, currentID uint) (string, error) {
	list, err := channels.List()
	if err != nil {
		return "", err
	}
	used := make(map[string]uint, len(list))
	for _, item := range list {
		used[item.Name] = item.ID
	}
	desired = truncateRunes(desired, 128)
	if owner, exists := used[desired]; !exists || owner == currentID {
		return desired, nil
	}

	siteID := externalID
	if index := strings.LastIndex(externalID, ":"); index >= 0 && index+1 < len(externalID) {
		siteID = externalID[index+1:]
	}
	siteID = truncateRunes(siteID, 64)
	for attempt := 1; attempt <= len(list)+1; attempt++ {
		discriminator := siteID
		if attempt > 1 {
			discriminator = fmt.Sprintf("%s-%d", siteID, attempt)
		}
		suffix := fmt.Sprintf(" [Toporeduce #%s]", discriminator)
		candidate := truncateRunes(desired, 128-len([]rune(suffix))) + suffix
		if owner, exists := used[candidate]; !exists || owner == currentID {
			return candidate, nil
		}
	}
	return "", errors.New("could not allocate a unique managed channel name")
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func bearerCredential(header string) (string, bool) {
	scheme, credential, ok := strings.Cut(strings.TrimSpace(header), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	credential = strings.TrimSpace(credential)
	return credential, credential != ""
}
