package services

import (
	"hive/internal/appstate"
	"hive/internal/db"
	"hive/internal/ingestion"
)

// SettingService exposes the key/value settings store the frontend mirrors its
// preferences into.
type SettingService struct {
	app *appstate.State
}

// ApplyNetworkSettings rebuilds the HTTP client from the persisted proxy /
// timeout settings so the change takes effect without an app restart.
func (s *SettingService) ApplyNetworkSettings() error {
	ctx := bg()
	timeout := db.SettingInt(ctx, s.app.DB.R, "net_timeout_sec", 30)
	proxy := "system"
	if v, _ := db.GetSetting(ctx, s.app.DB.R, "net_proxy"); v != nil {
		proxy = *v
	}
	s.app.SetHTTP(ingestion.BuildClient(timeout, proxy))
	return nil
}

// GetSetting returns a setting's value, or null when the key is absent.
func (s *SettingService) GetSetting(key string) (*string, error) {
	return db.GetSetting(bg(), s.app.DB.R, key)
}

// SetSetting upserts a setting value.
func (s *SettingService) SetSetting(key, value string) error {
	return db.SetSetting(bg(), s.app.DB.W, key, value)
}
