package db

import (
	"context"
	"strings"

	"hive/internal/apperr"
	"hive/internal/models"
)

const ruleCols = "id, name, enabled, feed_id, field, query, action, position"

// scanRules collects Rule rows from a rules query.
func scanRules(ctx context.Context, q Querier, sqlText string) ([]models.Rule, error) {
	rows, err := q.QueryContext(ctx, sqlText)
	if err != nil {
		return nil, apperr.Wrap("db", err)
	}
	defer rows.Close()
	out := []models.Rule{}
	for rows.Next() {
		var r models.Rule
		if err := rows.Scan(&r.ID, &r.Name, &r.Enabled, &r.FeedID, &r.Field, &r.Query, &r.Action, &r.Position); err != nil {
			return nil, apperr.Wrap("db", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListRules returns every rule, enabled or not, ordered for the settings list.
func ListRules(ctx context.Context, q Querier) ([]models.Rule, error) {
	return scanRules(ctx, q, "SELECT "+ruleCols+" FROM rules ORDER BY position, id")
}

// ActiveRules returns only the enabled rules — the set evaluated against
// incoming articles.
func ActiveRules(ctx context.Context, q Querier) ([]models.Rule, error) {
	return scanRules(ctx, q, "SELECT "+ruleCols+" FROM rules WHERE enabled = 1 ORDER BY position, id")
}

// CreateRule inserts a new rule at the end of the list and returns its id.
func CreateRule(ctx context.Context, q Querier, name string, feedID *int64, field, query, action string) (int64, error) {
	// MAX(position)+1 so a deleted middle rule's gap does not collide; rule
	// order is load-bearing (active_rules evaluates in order, skip short-
	// circuits).
	var next int64
	if err := q.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(position), -1) + 1 FROM rules").Scan(&next); err != nil {
		return 0, apperr.Wrap("db", err)
	}
	res, err := q.ExecContext(ctx,
		`INSERT INTO rules(name, feed_id, field, query, action, position)
		 VALUES (?1, ?2, ?3, ?4, ?5, ?6)`,
		name, nz(feedID), field, query, action, next)
	if err != nil {
		return 0, apperr.Wrap("db", err)
	}
	return res.LastInsertId()
}

// UpdateRule replaces a rule's editable fields.
func UpdateRule(ctx context.Context, q Querier, id int64, name string, enabled bool, feedID *int64, field, query, action string) error {
	if _, err := q.ExecContext(ctx,
		`UPDATE rules SET name = ?2, enabled = ?3, feed_id = ?4,
						  field = ?5, query = ?6, action = ?7
		 WHERE id = ?1`,
		id, name, enabled, nz(feedID), field, query, action); err != nil {
		return apperr.Wrap("db", err)
	}
	return nil
}

// DeleteRule removes a rule.
func DeleteRule(ctx context.Context, q Querier, id int64) error {
	if _, err := q.ExecContext(ctx, "DELETE FROM rules WHERE id = ?1", id); err != nil {
		return apperr.Wrap("db", err)
	}
	return nil
}

// ruleMatchWhere builds the WHERE fragment (and its bind values) that selects
// the articles a rule matches: one unicode_lower(col) LIKE ? per (keyword ×
// column), OR-joined, optionally scoped to one feed. Columns are unaliased so
// the fragment slots into a bare SELECT / UPDATE / DELETE on articles. Returns
// ok=false when the query holds no usable keywords.
//
// The column side is folded with unicode_lower (not SQLite's ASCII-only LOWER)
// so it matches the Unicode-aware ToLower ruleMatches applies at ingestion. LIKE
// wildcards in a keyword are escaped so a literal % / _ cannot widen the match.
func ruleMatchWhere(field, query string, feedID *int64) (string, []any, bool) {
	var terms []string
	for _, t := range strings.Split(query, ",") {
		t = strings.ToLower(strings.TrimSpace(t))
		if t != "" {
			terms = append(terms, t)
		}
	}
	if len(terms) == 0 {
		return "", nil, false
	}
	var cols []string
	switch field {
	case "author":
		cols = []string{"author"}
	case "content":
		cols = []string{"body_text"}
	case "any":
		cols = []string{"title", "author", "body_text"}
	default:
		cols = []string{"title"}
	}
	var ors []string
	var binds []any
	for _, term := range terms {
		escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(term)
		for _, col := range cols {
			ors = append(ors, "unicode_lower(COALESCE("+col+",'')) LIKE ? ESCAPE '\\'")
			binds = append(binds, "%"+escaped+"%")
		}
	}
	where := "(" + strings.Join(ors, " OR ") + ")"
	if feedID != nil {
		where += " AND feed_id = ?"
		binds = append(binds, *feedID)
	}
	return where, binds, true
}

// PreviewRule previews how a draft rule would behave: the number of
// already-stored articles its keywords match, plus up to five recent sample
// titles.
func PreviewRule(ctx context.Context, q Querier, feedID *int64, field, query string) (int64, []string, error) {
	where, binds, ok := ruleMatchWhere(field, query, feedID)
	if !ok {
		return 0, []string{}, nil
	}
	var count int64
	if err := q.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM articles WHERE "+where, binds...).Scan(&count); err != nil {
		return 0, nil, apperr.Wrap("db", err)
	}
	rows, err := q.QueryContext(ctx,
		"SELECT title FROM articles WHERE "+where+
			" ORDER BY datetime(COALESCE(published_at, fetched_at)) DESC, id DESC LIMIT 5",
		binds...)
	if err != nil {
		return 0, nil, apperr.Wrap("db", err)
	}
	defer rows.Close()
	samples := []string{}
	for rows.Next() {
		var title string
		if err := rows.Scan(&title); err != nil {
			return 0, nil, apperr.Wrap("db", err)
		}
		samples = append(samples, title)
	}
	return count, samples, rows.Err()
}

// ApplyRuleToExisting applies a saved rule's action to the articles already in
// the store that it matches — the one-time backfill run when a rule is created
// or edited. A skip rule deletes its matches; read / star set the flag on rows
// that lack it. Returns the number of articles acted on.
func ApplyRuleToExisting(ctx context.Context, q Querier, feedID *int64, field, query, action string) (int64, error) {
	where, binds, ok := ruleMatchWhere(field, query, feedID)
	if !ok {
		return 0, nil
	}
	var sqlText string
	switch action {
	case "skip":
		sqlText = "DELETE FROM articles WHERE " + where
	case "read":
		sqlText = "UPDATE articles SET is_read = 1 WHERE (" + where + ") AND is_read = 0"
	case "star":
		sqlText = "UPDATE articles SET is_starred = 1 WHERE (" + where + ") AND is_starred = 0"
	default:
		return 0, nil
	}
	res, err := q.ExecContext(ctx, sqlText, binds...)
	if err != nil {
		return 0, apperr.Wrap("db", err)
	}
	return res.RowsAffected()
}
