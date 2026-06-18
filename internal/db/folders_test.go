package db

import (
	"context"
	"errors"
	"testing"

	"hive/internal/apperr"
)

func TestFolderCRUD(t *testing.T) {
	d := openTemp(t)
	ctx := context.Background()

	id, err := CreateFolder(ctx, d.W, "  Tech  ")
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}

	// Case-insensitive idempotency: same name in another case reuses the row.
	id2, err := CreateFolder(ctx, d.W, "tech")
	if err != nil {
		t.Fatalf("CreateFolder dup: %v", err)
	}
	if id != id2 {
		t.Errorf("dedup failed: got new id %d, want %d", id2, id)
	}

	folders, err := ListFolders(ctx, d.R)
	if err != nil {
		t.Fatalf("ListFolders: %v", err)
	}
	if len(folders) != 1 {
		t.Fatalf("len(folders) = %d, want 1", len(folders))
	}
	if folders[0].Name != "Tech" {
		t.Errorf("name = %q, want trimmed %q", folders[0].Name, "Tech")
	}

	// Empty name is rejected with a coded error.
	if _, err := CreateFolder(ctx, d.W, "   "); err == nil {
		t.Error("expected emptyFolderName error")
	} else {
		var ae *apperr.AppError
		if !errors.As(err, &ae) || ae.Code != "emptyFolderName" {
			t.Errorf("got %v, want code emptyFolderName", err)
		}
	}

	// Rename clash against a different folder is rejected.
	other, _ := CreateFolder(ctx, d.W, "News")
	if err := RenameFolder(ctx, d.W, other, "tech"); err == nil {
		t.Error("expected folderNameExists error")
	}

	// Rename to a fresh name works.
	if err := RenameFolder(ctx, d.W, other, "Headlines"); err != nil {
		t.Fatalf("RenameFolder: %v", err)
	}

	if err := DeleteFolder(ctx, d.W, id); err != nil {
		t.Fatalf("DeleteFolder: %v", err)
	}
	folders, _ = ListFolders(ctx, d.R)
	if len(folders) != 1 {
		t.Errorf("after delete len(folders) = %d, want 1", len(folders))
	}
}

// Per-feed open mode: nil follows the global default; set → listed → cleared.
func TestOpenModeRoundTripsAndClears(t *testing.T) {
	d, feedID, _ := seed(t)
	ctx := context.Background()

	feeds, err := ListFeeds(ctx, d.R)
	if err != nil {
		t.Fatal(err)
	}
	if feeds[0].OpenMode != nil {
		t.Fatalf("fresh feed open mode = %v, want nil", *feeds[0].OpenMode)
	}

	web := "web"
	if err := SetFeedOpenMode(ctx, d.W, feedID, &web); err != nil {
		t.Fatal(err)
	}
	feeds, _ = ListFeeds(ctx, d.R)
	if feeds[0].OpenMode == nil || *feeds[0].OpenMode != "web" {
		t.Fatalf("open mode = %v, want web", feeds[0].OpenMode)
	}

	if err := SetFeedOpenMode(ctx, d.W, feedID, nil); err != nil {
		t.Fatal(err)
	}
	feeds, _ = ListFeeds(ctx, d.R)
	if feeds[0].OpenMode != nil {
		t.Fatalf("cleared open mode = %v, want nil", *feeds[0].OpenMode)
	}
}
