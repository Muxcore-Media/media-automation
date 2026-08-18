package internal

import (
	"context"
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
	stallMins := m.stallTimeoutMinutes
	stallAuto := m.stallAutoMode
	stallLoops := stallLoopCSV(m.stallLoopMinutes)
	keepPartials := m.keepStalledPartials
	m.mu.RUnlock()
	overridesJSON, _ := m.listSeriesOverridesJSON(context.Background())
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
		{
			Key:         "stall_timeout_minutes",
			Label:       "Stall timeout (minutes)",
			Type:        contracts.SettingTypeInt,
			Default:     strconv.Itoa(defaultStallTimeoutMinutes),
			Value:       strconv.Itoa(stallMins),
			Description: "Give up on a torrent with no progress after this many minutes when auto mode is off. Then blacklist it and try the next release.",
			Group:       "Stall",
		},
		{
			Key:         "stall_auto_mode",
			Label:       "Stall auto mode",
			Type:        contracts.SettingTypeBool,
			Default:     "true",
			Value:       strconv.FormatBool(stallAuto),
			Description: "After every available torrent has been tried, start a new loop with a longer stall timeout (see stall_loop_minutes).",
			Group:       "Stall",
		},
		{
			Key:         "stall_loop_minutes",
			Label:       "Stall loop minutes",
			Type:        contracts.SettingTypeString,
			Default:     defaultStallLoopCSV,
			Value:       stallLoops,
			Description: "Comma-separated stall minutes per attempt loop when auto mode is on. Default 60,360 (1h then 6h). After the last value, later loops keep that timeout.",
			Group:       "Stall",
		},
		{
			Key:         "keep_stalled_partials",
			Label:       "Keep stalled partials",
			Type:        contracts.SettingTypeBool,
			Default:     "false",
			Value:       strconv.FormatBool(keepPartials),
			Description: "Do not delete torrent data when a grab stalls or fails. Uses extra disk until a complete copy is imported, then leftover partial dirs for that item are removed. Lets later loops resume matching hashes and share verified on-disk bytes.",
			Group:       "Stall",
		},
		{
			Key:         "series_overrides_json",
			Label:       "Per-series overrides (JSON)",
			Type:        contracts.SettingTypeString,
			Default:     "[]",
			Value:       overridesJSON,
			Description: `JSON array: [{"series_id":"…","delay_minutes":60,"preferred_groups":["FLUX"],"ignored_groups":["RARBG"]}]. Replaces the full table on update.`,
			Group:       "Overrides",
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
	case "stall_timeout_minutes":
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || n < 1 {
			return fmt.Errorf("invalid stall_timeout_minutes")
		}
		m.mu.Lock()
		m.stallTimeoutMinutes = n
		m.mu.Unlock()
		return nil
	case "stall_auto_mode":
		m.mu.Lock()
		m.stallAutoMode = value == "true" || value == "1" || value == "on"
		m.mu.Unlock()
		return nil
	case "stall_loop_minutes":
		loops, err := parseStallLoopMinutes(value)
		if err != nil {
			return err
		}
		m.mu.Lock()
		m.stallLoopMinutes = loops
		m.mu.Unlock()
		return nil
	case "keep_stalled_partials":
		m.mu.Lock()
		m.keepStalledPartials = value == "true" || value == "1" || value == "on"
		m.mu.Unlock()
		return nil
	case "series_overrides_json", "AUTOMATION_SERIES_OVERRIDES_JSON":
		m.mu.RLock()
		db := m.db
		m.mu.RUnlock()
		if db == nil {
			return fmt.Errorf("database not ready")
		}
		return m.replaceSeriesOverridesJSON(context.Background(), db, value)
	default:
		return fmt.Errorf("unknown setting %q", key)
	}
}

func (m *Module) Settings() []contracts.SettingDef {
	return m.settingsDefs()
}

func (m *Module) UpdateSetting(key, value string) error {
	return m.updateSetting(key, value)
}

func (m *Module) registerSettingsMesh(srv *grpc.Server) {
	modulesdk.RegisterSettings(srv, m.id, m)
}
