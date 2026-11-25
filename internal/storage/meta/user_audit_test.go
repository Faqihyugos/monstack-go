package meta_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Faqihyugos/monstack-go/internal/config"
	"github.com/Faqihyugos/monstack-go/internal/storage/meta"
	"github.com/Faqihyugos/monstack-go/pkg/models"
)

func TestUserAndAuditRepositories(t *testing.T) {
	dir := t.TempDir()
	db, err := meta.Open(config.MetadataConfig{
		Path:        filepath.Join(dir, "meta.db"),
		WALMode:     true,
		BusyTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("meta.Open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()

	// Users CRUD
	u := &models.User{
		ID:           "usr-1",
		Username:     "ops-admin",
		PasswordHash: "$2a$10$dummyhash",
		Role:         models.RoleAdmin,
		APIKey:       "ms_live_key_123",
	}
	if err := db.CreateUser(ctx, u); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	gotByName, err := db.GetUserByUsername(ctx, "ops-admin")
	if err != nil || gotByName.Role != models.RoleAdmin {
		t.Fatalf("GetUserByUsername failed: %+v, err=%v", gotByName, err)
	}

	gotByKey, err := db.GetUserByAPIKey(ctx, "ms_live_key_123")
	if err != nil || gotByKey.ID != "usr-1" {
		t.Fatalf("GetUserByAPIKey failed: %+v, err=%v", gotByKey, err)
	}

	users, err := db.ListUsers(ctx)
	if err != nil || len(users) != 1 {
		t.Fatalf("expected 1 user, got %d (err=%v)", len(users), err)
	}

	// Audit Log
	entry := &models.AuditEntry{
		Actor:      "ops-admin",
		Action:     "create",
		Resource:   "target",
		ResourceID: "prod-api",
		Details:    "created HTTP target",
		IPAddress:  "127.0.0.1",
	}
	if err := db.RecordAudit(ctx, entry); err != nil {
		t.Fatalf("RecordAudit: %v", err)
	}
	if entry.ID == 0 {
		t.Errorf("expected auto-incremented audit ID")
	}

	logs, err := db.ListAuditLogs(ctx, "target", 10)
	if err != nil || len(logs) != 1 || logs[0].ResourceID != "prod-api" {
		t.Fatalf("ListAuditLogs unexpected result: %+v (err=%v)", logs, err)
	}

	if err := db.DeleteUser(ctx, "usr-1"); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
}
