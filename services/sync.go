package services

import (
	"hive/internal/appstate"
	"hive/internal/events"
	"hive/internal/syncer"
)

// SyncService exposes the GReader (FreshRSS / Miniflux) sync commands.
type SyncService struct {
	app *appstate.State
}

// FreshrssConnect links a GReader-compatible server, verifying the credentials
// and storing an auth token (never the password). provider is "freshrss" or
// "miniflux".
func (s *SyncService) FreshrssConnect(url, username, password, provider string) error {
	return syncer.Connect(bg(), s.app.HTTP(), s.app.DB, url, username, password, provider)
}

// FreshrssDisconnect clears the stored sync credentials.
func (s *SyncService) FreshrssDisconnect() error {
	return syncer.Disconnect(bg(), s.app.DB.W)
}

// FreshrssStatus reports whether a server is linked, its URL and provider.
func (s *SyncService) FreshrssStatus() (syncer.Status, error) {
	return syncer.GetStatus(bg(), s.app.DB.R)
}

// FreshrssSync runs a full sync now and returns the number of reconciled
// articles, emitting feeds-updated so the sidebar refreshes.
func (s *SyncService) FreshrssSync() (int, error) {
	n, err := syncer.Sync(bg(), s.app.HTTP(), s.app.DB)
	if err == nil {
		events.Emit("feeds-updated", 0)
	}
	return n, err
}
