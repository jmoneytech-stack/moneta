package store

import (
	"context"
	"testing"
)

func TestEnsureDefaultEntityBootstrapsFreshDatabaseIdempotently(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	firstID, err := EnsureDefaultEntity(ctx, db)
	if err != nil {
		t.Fatalf("EnsureDefaultEntity() first call error: %v", err)
	}
	secondID, err := EnsureDefaultEntity(ctx, db)
	if err != nil {
		t.Fatalf("EnsureDefaultEntity() second call error: %v", err)
	}
	if firstID <= 0 || secondID != firstID {
		t.Errorf("default entity ids = %d/%d, want one positive id", firstID, secondID)
	}

	var count int
	var kind, name string
	if err := db.QueryRow(`
		SELECT count(*), kind, name
		FROM entities
	`).Scan(&count, &kind, &name); err != nil {
		t.Fatalf("read default entity: %v", err)
	}
	if count != 1 || kind != "personal" || name != "Personal" {
		t.Errorf("default entity = count %d, kind %q, name %q", count, kind, name)
	}
}

func TestEnsureDefaultEntityValidatesDatabase(t *testing.T) {
	if _, err := EnsureDefaultEntity(context.Background(), nil); err == nil {
		t.Fatal("EnsureDefaultEntity() accepted a nil database")
	}
}

func TestEnsureDefaultEntityReusesExistingPersonalEntity(t *testing.T) {
	db := openTestDB(t)
	wantID := insertEntity(t, db, "personal", "Existing Personal")

	gotID, err := EnsureDefaultEntity(context.Background(), db)
	if err != nil {
		t.Fatalf("EnsureDefaultEntity() error: %v", err)
	}
	if gotID != wantID {
		t.Errorf("default entity id = %d, want existing id %d", gotID, wantID)
	}

	var count int
	if err := db.QueryRow("SELECT count(*) FROM entities WHERE kind = 'personal'").Scan(&count); err != nil {
		t.Fatalf("count personal entities: %v", err)
	}
	if count != 1 {
		t.Errorf("personal entity count = %d, want 1", count)
	}
}
