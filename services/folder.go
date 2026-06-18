package services

import (
	"hive/internal/appstate"
	"hive/internal/db"
	"hive/internal/models"
)

// FolderService exposes folder CRUD. Mirrors the folder commands in commands.rs.
type FolderService struct {
	app *appstate.State
}

// ListFolders returns every folder ordered for the sidebar.
func (s *FolderService) ListFolders() ([]models.Folder, error) {
	return db.ListFolders(bg(), s.app.DB.R)
}

// CreateFolder creates a folder (idempotent on name) and returns its id.
func (s *FolderService) CreateFolder(name string) (int64, error) {
	return db.CreateFolder(bg(), s.app.DB.W, name)
}

// RenameFolder renames a folder, rejecting a clash with a different folder.
func (s *FolderService) RenameFolder(id int64, name string) error {
	return db.RenameFolder(bg(), s.app.DB.W, id, name)
}

// DeleteFolder removes a folder; its feeds become unfiled.
func (s *FolderService) DeleteFolder(id int64) error {
	return db.DeleteFolder(bg(), s.app.DB.W, id)
}
