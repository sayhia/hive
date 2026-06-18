package db

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"hive/internal/apperr"
	"hive/internal/models"
)

// tagColors are the palette keys cycled through as new tags are created.
var tagColors = []string{
	"clay", "amber", "pine", "teal", "indigo", "violet", "rose", "slate",
}

// ListTags returns every tag, ordered for the sidebar, with a live article count.
func ListTags(ctx context.Context, q Querier) ([]models.Tag, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT t.id, t.name, t.color, t.position,
				(SELECT COUNT(*) FROM article_tags at WHERE at.tag_id = t.id)
		 FROM tags t ORDER BY t.position, t.name COLLATE NOCASE`)
	if err != nil {
		return nil, apperr.Wrap("db", err)
	}
	defer rows.Close()
	out := []models.Tag{}
	for rows.Next() {
		var t models.Tag
		if err := rows.Scan(&t.ID, &t.Name, &t.Color, &t.Position, &t.ArticleCount); err != nil {
			return nil, apperr.Wrap("db", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// CreateTag creates a tag, auto-assigning the next palette colour and list
// position, or returns the existing tag when the name already exists
// (case-insensitively). Mirrors CreateFolder's dedup.
func CreateTag(ctx context.Context, q Querier, name string) (int64, error) {
	name = strings.TrimSpace(name)
	var id int64
	err := q.QueryRowContext(ctx,
		"SELECT id FROM tags WHERE name = ?1 COLLATE NOCASE", name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, apperr.Wrap("db", err)
	}
	// MAX(position)+1 — not COUNT(*) — so a deleted middle tag's gap does not
	// collide; the colour cycles off the same index.
	var next int64
	if err := q.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(position), -1) + 1 FROM tags").Scan(&next); err != nil {
		return 0, apperr.Wrap("db", err)
	}
	color := tagColors[next%int64(len(tagColors))]
	res, err := q.ExecContext(ctx,
		"INSERT INTO tags(name, color, position) VALUES (?1, ?2, ?3)", name, color, next)
	if err != nil {
		return 0, apperr.Wrap("db", err)
	}
	return res.LastInsertId()
}

// RenameTag renames a tag, rejecting a name that collides with a different
// existing tag (case-insensitively). Renaming a tag to its own name is allowed.
func RenameTag(ctx context.Context, q Querier, id int64, name string) error {
	name = strings.TrimSpace(name)
	var clash int64
	err := q.QueryRowContext(ctx,
		"SELECT id FROM tags WHERE name = ?1 COLLATE NOCASE AND id != ?2", name, id).Scan(&clash)
	if err == nil {
		return apperr.Code("tagNameExists")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return apperr.Wrap("db", err)
	}
	if _, err := q.ExecContext(ctx, "UPDATE tags SET name = ?2 WHERE id = ?1", id, name); err != nil {
		return apperr.Wrap("db", err)
	}
	return nil
}

// SetTagColor changes a tag's colour (a palette key).
func SetTagColor(ctx context.Context, q Querier, id int64, color string) error {
	if _, err := q.ExecContext(ctx, "UPDATE tags SET color = ?2 WHERE id = ?1", id, color); err != nil {
		return apperr.Wrap("db", err)
	}
	return nil
}

// ReorderTags persists a new tag ordering — ids listed in the desired display
// order — in one transaction.
func ReorderTags(ctx context.Context, db *DB, ids []int64) error {
	tx, err := db.W.BeginTx(ctx, nil)
	if err != nil {
		return apperr.Wrap("db", err)
	}
	defer tx.Rollback()
	for pos, id := range ids {
		if _, err := tx.ExecContext(ctx,
			"UPDATE tags SET position = ?2 WHERE id = ?1", id, int64(pos)); err != nil {
			return apperr.Wrap("db", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return apperr.Wrap("db", err)
	}
	return nil
}

// DeleteTag removes a tag; its article_tags rows cascade.
func DeleteTag(ctx context.Context, q Querier, id int64) error {
	if _, err := q.ExecContext(ctx, "DELETE FROM tags WHERE id = ?1", id); err != nil {
		return apperr.Wrap("db", err)
	}
	return nil
}

// SetArticleTag attaches (on = true) or detaches a tag from one article.
func SetArticleTag(ctx context.Context, q Querier, articleID, tagID int64, on bool) error {
	var err error
	if on {
		_, err = q.ExecContext(ctx,
			"INSERT INTO article_tags(article_id, tag_id) VALUES (?1, ?2) ON CONFLICT DO NOTHING",
			articleID, tagID)
	} else {
		_, err = q.ExecContext(ctx,
			"DELETE FROM article_tags WHERE article_id = ?1 AND tag_id = ?2", articleID, tagID)
	}
	if err != nil {
		return apperr.Wrap("db", err)
	}
	return nil
}

// TagsForArticle returns the tags attached to one article (ArticleCount left at
// 0 — unused per-article).
func TagsForArticle(ctx context.Context, q Querier, articleID int64) ([]models.Tag, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT t.id, t.name, t.color, t.position
		 FROM tags t JOIN article_tags at ON at.tag_id = t.id
		 WHERE at.article_id = ?1 ORDER BY t.position, t.name COLLATE NOCASE`, articleID)
	if err != nil {
		return nil, apperr.Wrap("db", err)
	}
	defer rows.Close()
	out := []models.Tag{}
	for rows.Next() {
		var t models.Tag
		if err := rows.Scan(&t.ID, &t.Name, &t.Color, &t.Position); err != nil {
			return nil, apperr.Wrap("db", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
