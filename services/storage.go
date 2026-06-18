package services

import (
	"hive/internal/appstate"
	"hive/internal/db"
	"hive/internal/models"
)

// StorageService exposes the storage / data-management commands in the settings
// "Advanced" panel.
type StorageService struct {
	app *appstate.State
}

// StorageStats returns database size and content counts.
func (s *StorageService) StorageStats() (models.StorageStats, error) {
	st, err := db.StorageStats(bg(), s.app.DB.R)
	if err != nil {
		return models.StorageStats{}, err
	}
	return models.StorageStats{
		DBBytes:      st.DBBytes,
		ArticleCount: st.ArticleCount,
		FeedCount:    st.FeedCount,
	}, nil
}

// CleanupArticles deletes read articles older than days (keeping starred,
// read-later and highlighted ones) and returns how many were removed.
func (s *StorageService) CleanupArticles(days int64) (int64, error) {
	return db.CleanupOldArticles(bg(), s.app.DB, days)
}

// VacuumDB reclaims free database pages.
func (s *StorageService) VacuumDB() error {
	return db.Vacuum(bg(), s.app.DB.W)
}

// ResetSettings clears every stored setting.
func (s *StorageService) ResetSettings() error {
	return db.ResetSettings(bg(), s.app.DB.W)
}

// ClearAllData wipes all feeds, articles and folders (settings are kept).
func (s *StorageService) ClearAllData() error {
	return db.ClearAllData(bg(), s.app.DB)
}
