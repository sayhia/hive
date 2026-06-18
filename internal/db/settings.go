package db

import (
	"context"
	"database/sql"
	"errors"
	"strconv"

	"hive/internal/apperr"
)

// GetSetting returns a setting's value, or nil when the key is absent.
func GetSetting(ctx context.Context, q Querier, key string) (*string, error) {
	var v string
	err := q.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?1", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, apperr.Wrap("db", err)
	}
	return &v, nil
}

// SetSetting upserts a setting value.
func SetSetting(ctx context.Context, q Querier, key, value string) error {
	if _, err := q.ExecContext(ctx,
		`INSERT INTO settings(key, value) VALUES (?1, ?2)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value); err != nil {
		return apperr.Wrap("db", err)
	}
	return nil
}

// SettingInt reads a setting and parses it as an int64, falling back to
// fallback when the key is missing, unreadable, or fails to parse.
func SettingInt(ctx context.Context, q Querier, key string, fallback int64) int64 {
	v, err := GetSetting(ctx, q, key)
	if err != nil || v == nil {
		return fallback
	}
	n, err := strconv.ParseInt(*v, 10, 64)
	if err != nil {
		return fallback
	}
	return n
}

// SettingFlag reads a setting as a boolean flag — "1" and "true" are true,
// anything else (including a missing key) falls back to fallback.
func SettingFlag(ctx context.Context, q Querier, key string, fallback bool) bool {
	v, err := GetSetting(ctx, q, key)
	if err != nil || v == nil {
		return fallback
	}
	return *v == "1" || *v == "true"
}
