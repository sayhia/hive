package services

import (
	"hive/internal/appstate"
	"hive/internal/db"
	"hive/internal/models"
)

// RuleService exposes filter-rule CRUD plus the preview / apply-to-existing
// helpers the settings UI uses.
type RuleService struct {
	app *appstate.State
}

// ListRules returns every rule, enabled or not, ordered for the settings list.
func (s *RuleService) ListRules() ([]models.Rule, error) {
	return db.ListRules(bg(), s.app.DB.R)
}

// CreateRule inserts a new rule at the end of the list and returns its id.
func (s *RuleService) CreateRule(name string, feedID *int64, field, query, action string) (int64, error) {
	return db.CreateRule(bg(), s.app.DB.W, name, feedID, field, query, action)
}

// UpdateRule replaces a rule's editable fields.
func (s *RuleService) UpdateRule(id int64, name string, enabled bool, feedID *int64, field, query, action string) error {
	return db.UpdateRule(bg(), s.app.DB.W, id, name, enabled, feedID, field, query, action)
}

// DeleteRule removes a rule.
func (s *RuleService) DeleteRule(id int64) error {
	return db.DeleteRule(bg(), s.app.DB.W, id)
}

// PreviewRule reports how many already-stored articles a draft rule matches,
// with up to five recent sample titles.
func (s *RuleService) PreviewRule(feedID *int64, field, query string) (models.RulePreview, error) {
	count, samples, err := db.PreviewRule(bg(), s.app.DB.R, feedID, field, query)
	if err != nil {
		return models.RulePreview{}, err
	}
	return models.RulePreview{Count: count, Samples: samples}, nil
}

// ApplyRuleToExisting applies a rule's action to the already-stored articles it
// matches, returning the number acted on. A skip rule deletes its matches.
func (s *RuleService) ApplyRuleToExisting(feedID *int64, field, query, action string) (int64, error) {
	return db.ApplyRuleToExisting(bg(), s.app.DB.W, feedID, field, query, action)
}
