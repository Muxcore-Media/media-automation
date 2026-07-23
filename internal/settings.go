package internal

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Muxcore-Media/core/pkg/contracts"
	modulesdk "github.com/Muxcore-Media/core/sdk/go/module"
	"google.golang.org/grpc"
)

func (m *Module) settingsDefs() []contracts.SettingDef {
	m.mu.RLock()
	search := m.enableAutomaticSearch
	upgrades := m.enableAutomaticUpgrades
	rss := m.rssSyncMinutes
	m.mu.RUnlock()
	return []contracts.SettingDef{
		{
			Key:         "enable_automatic_search",
			Label:       "Automatic Search",
			Type:        contracts.SettingTypeBool,
			Default:     "true",
			Value:       strconv.FormatBool(search),
			Description: "Periodically search for wanted media",
			Group:       "Automation",
		},
		{
			Key:         "enable_automatic_upgrades",
			Label:       "Automatic Upgrades",
			Type:        contracts.SettingTypeBool,
			Default:     "true",
			Value:       strconv.FormatBool(upgrades),
			Description: "Allow quality upgrades when profile permits",
			Group:       "Automation",
		},
		{
			Key:         "rss_sync_minutes",
			Label:       "RSS Sync Interval (minutes)",
			Type:        contracts.SettingTypeInt,
			Default:     "15",
			Value:       strconv.Itoa(rss),
			Description: "How often to sync RSS / wanted searches",
			Group:       "Automation",
		},
	}
}

func (m *Module) updateSetting(key, value string) error {
	switch key {
	case "enable_automatic_search":
		m.mu.Lock()
		m.enableAutomaticSearch = value == "true" || value == "1" || value == "on"
		m.mu.Unlock()
		return nil
	case "enable_automatic_upgrades":
		m.mu.Lock()
		m.enableAutomaticUpgrades = value == "true" || value == "1" || value == "on"
		m.mu.Unlock()
		return nil
	case "rss_sync_minutes":
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || n < 1 {
			return fmt.Errorf("invalid rss_sync_minutes")
		}
		m.mu.Lock()
		m.rssSyncMinutes = n
		m.mu.Unlock()
		return nil
	default:
		return fmt.Errorf("unknown setting %q", key)
	}
}

func (m *Module) registerSettingsMesh(srv *grpc.Server) {
	modulesdk.RegisterMeshHandler(srv, m.id, modulesdk.SettingsHandler{
		List:   m.settingsDefs,
		Update: m.updateSetting,
	})
}
