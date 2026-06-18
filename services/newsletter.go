package services

import (
	"context"
	"fmt"
	"strings"
	"time"

	"hive/internal/apperr"
	"hive/internal/appstate"
	"hive/internal/db"
	"hive/internal/models"
	"hive/internal/newsletter"
)

// NewsletterService exposes the email-newsletter (IMAP-polled) source commands.
type NewsletterService struct {
	app *appstate.State
}

// newsletterPollTimeout bounds the IMAP probe so a server that completes the
// handshake but stalls mid-command can't hang the Add dialog forever.
const newsletterPollTimeout = 60 * time.Second

// AddNewsletterSource verifies an IMAP mailbox by connecting, then persists it
// as a feed (source_type='newsletter') plus its credentials, ingests the
// messages just fetched, and returns the new feed.
func (s *NewsletterService) AddNewsletterSource(input models.NewsletterInput) (*models.Feed, error) {
	ctx := bg()
	folder := strings.TrimSpace(input.Folder)
	if folder == "" {
		folder = "INBOX"
	}
	cfg := models.NewsletterConfig{
		Host:     strings.TrimSpace(input.Host),
		Port:     uint16(input.Port),
		Username: strings.TrimSpace(input.Username),
		Password: input.Password,
		Folder:   folder,
	}
	if cfg.Host == "" || cfg.Username == "" || cfg.Password == "" {
		return nil, apperr.Code("newsletterMissingFields")
	}
	feedURL := fmt.Sprintf("imap://%s@%s:%d/%s", cfg.Username, cfg.Host, cfg.Port, cfg.Folder)
	title := cfg.Username
	if input.Title != nil && strings.TrimSpace(*input.Title) != "" {
		title = strings.TrimSpace(*input.Title)
	}

	// Reject a duplicate mailbox before the (slow) IMAP round-trip.
	if existing, err := db.FindFeedByURL(ctx, s.app.DB.R, feedURL); err != nil {
		return nil, err
	} else if existing != nil {
		return nil, apperr.Code("alreadySubscribed")
	}

	// Verify the credentials by actually connecting, bounded by a wall-clock cap.
	pollCtx, cancel := context.WithTimeout(ctx, newsletterPollTimeout)
	defer cancel()
	messages, err := newsletter.Poll(pollCtx, cfg)
	if err != nil {
		if pollCtx.Err() == context.DeadlineExceeded {
			return nil, apperr.Code("newsletterPollTimeout")
		}
		return nil, err
	}

	feedID, err := db.InsertNewsletterSource(ctx, s.app.DB, feedURL, title, cfg)
	if err != nil {
		return nil, err
	}
	rules, _ := db.ActiveRules(ctx, s.app.DB.R)
	var unread int64
	for i := range messages {
		if ok, _ := db.UpsertArticle(ctx, s.app.DB, feedID, &messages[i], false, rules); ok {
			unread++
		}
	}
	_ = db.TouchFeed(ctx, s.app.DB.W, feedID)
	lastFetched, _ := db.FeedLastFetched(ctx, s.app.DB.R, feedID)

	return &models.Feed{
		ID:            feedID,
		FeedURL:       feedURL,
		Title:         title,
		SourceType:    models.SourceNewsletter,
		LastFetchedAt: lastFetched,
		UnreadCount:   unread,
	}, nil
}

// ListNewsletterSources returns every configured newsletter source (passwords
// omitted).
func (s *NewsletterService) ListNewsletterSources() ([]models.NewsletterSource, error) {
	rows, err := db.ListNewsletterSources(bg(), s.app.DB.R)
	if err != nil {
		return nil, err
	}
	out := make([]models.NewsletterSource, 0, len(rows))
	for _, r := range rows {
		out = append(out, models.NewsletterSource{
			FeedID:   r.FeedID,
			Title:    r.Title,
			Host:     r.Host,
			Port:     int(r.Port),
			Username: r.Username,
			Folder:   r.Folder,
		})
	}
	return out, nil
}

// RemoveNewsletterSource removes a newsletter source and all of its articles.
func (s *NewsletterService) RemoveNewsletterSource(feedID int64) error {
	return db.DeleteNewsletterSource(bg(), s.app.DB.W, feedID)
}
