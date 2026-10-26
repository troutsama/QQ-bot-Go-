package store

import (
	"context"
	"path/filepath"
	"testing"

	"qq-bot/internal/config"
)

// 这个测试守住 T1 最关键的一条：SQL 与 user_version 必须同属一个事务。
// 如果像常见写法那样把 PRAGMA user_version 放到事务外，本测试会失败。
func TestValidateMigrationSequence_RejectsMissingVersion(t *testing.T) {
	cases := []struct {
		name    string
		files   []migration
		wantErr bool
	}{
		{
			name: "连续版本",
			files: []migration{
				{version: 1, name: "0001_init.sql"},
				{version: 2, name: "0002_users.sql"},
			},
		},
		{
			name:    "缺 0001",
			files:   []migration{{version: 2, name: "0002_users.sql"}},
			wantErr: true,
		},
		{
			name: "中间缺一版",
			files: []migration{
				{version: 1, name: "0001_init.sql"},
				{version: 3, name: "0003_users.sql"},
			},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateMigrationSequence(tc.files)
			if tc.wantErr && err == nil {
				t.Fatal("版本号有缺口时应返回错误")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("连续版本不应报错: %v", err)
			}
		})
	}
}

func TestApplyMigration_RollsBackOnFailure(t *testing.T) {
	cfg := config.DatabaseConfig{
		Driver:        "sqlite",
		Path:          filepath.Join(t.TempDir(), "test.db"),
		MaxOpenConns:  1,
		MaxIdleConns:  1,
		BusyTimeoutMS: 3000,
	}
	s, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ctx := context.Background()
	broken := migration{
		version: 1,
		name:    "0001_broken.sql",
		sql:     "CREATE TABLE half_done (id INTEGER); INSERT INTO no_such_table VALUES (1);",
	}
	if err := s.applyMigration(ctx, broken); err == nil {
		t.Fatal("残缺的迁移本应失败，但返回了 nil")
	}

	ver, err := s.MigrationVersion(ctx)
	if err != nil {
		t.Fatalf("MigrationVersion 失败: %v", err)
	}
	if ver != 0 {
		t.Fatalf("迁移失败后 user_version = %d，期望回滚到 0", ver)
	}

	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'half_done'`,
	).Scan(&n); err != nil {
		t.Fatalf("查询 half_done 失败: %v", err)
	}
	if n != 0 {
		t.Fatal("迁移失败后 half_done 表仍存在：DDL 没有随事务回滚")
	}
}
