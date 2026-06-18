package db

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"hive/internal/apperr"
	"hive/internal/models"
)

// ListFolders returns every folder ordered for the sidebar.
func ListFolders(ctx context.Context, q Querier) ([]models.Folder, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT id, name, position FROM folders ORDER BY position, name")
	if err != nil {
		return nil, apperr.Wrap("db", err)
	}
	defer rows.Close()
	out := []models.Folder{}
	for rows.Next() {
		var f models.Folder
		if err := rows.Scan(&f.ID, &f.Name, &f.Position); err != nil {
			return nil, apperr.Wrap("db", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// CreateFolder creates a folder, or returns the existing one when a folder with
// the same name (case-insensitively) is already present.
//
// folders.name carries no UNIQUE constraint, so without this guard two folders
// named "Tech" — or "Tech" and "tech" — could coexist. Idempotent-on-name is
// also what FolderIDByName (and so OPML import) wants. Mirrors CreateTag's
// case-insensitive dedup.
func CreateFolder(ctx context.Context, q Querier, name string) (int64, error) {
	// Trim before the dedup lookup and the insert: a name with surrounding
	// whitespace is a different string from its trimmed twin, so the COLLATE
	// NOCASE lookup would miss the existing folder and spawn the near-duplicate
	// the dedup exists to prevent.
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, apperr.Code("emptyFolderName")
	}
	var id int64
	err := q.QueryRowContext(ctx,
		"SELECT id FROM folders WHERE name = ?1 COLLATE NOCASE", name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, apperr.Wrap("db", err)
	}
	res, err := q.ExecContext(ctx,
		"INSERT INTO folders(name, position) VALUES (?1, (SELECT COALESCE(MAX(position),0)+1 FROM folders))",
		name)
	if err != nil {
		return 0, apperr.Wrap("db", err)
	}
	return res.LastInsertId()
}

// RenameFolder renames a folder, rejecting a name that collides with a
// different folder (case-insensitively). Renaming a folder to its own name (or
// a case change of it) is allowed. Mirrors RenameTag.
func RenameFolder(ctx context.Context, q Querier, id int64, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return apperr.Code("emptyFolderName")
	}
	var clash int64
	err := q.QueryRowContext(ctx,
		"SELECT id FROM folders WHERE name = ?1 COLLATE NOCASE AND id != ?2",
		name, id).Scan(&clash)
	if err == nil {
		return apperr.Code("folderNameExists")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return apperr.Wrap("db", err)
	}
	if _, err := q.ExecContext(ctx,
		"UPDATE folders SET name = ?2 WHERE id = ?1", id, name); err != nil {
		return apperr.Wrap("db", err)
	}
	return nil
}

// DeleteFolder removes a folder. Feeds in it have folder_id set to NULL
// (ON DELETE SET NULL).
func DeleteFolder(ctx context.Context, q Querier, id int64) error {
	if _, err := q.ExecContext(ctx, "DELETE FROM folders WHERE id = ?1", id); err != nil {
		return apperr.Wrap("db", err)
	}
	return nil
}

// FolderIDByName resolves a folder name to its id, creating the folder when
// absent. Used by OPML import to attach imported feeds to their folders.
func FolderIDByName(ctx context.Context, q Querier, name string) (int64, error) {
	return CreateFolder(ctx, q, name)
}
