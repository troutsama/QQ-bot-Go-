package store_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"qq-bot/internal/config"
	"qq-bot/internal/store"
)

// testConfig 为每个测试创建一个独立的 SQLite 文件（t.TempDir 自动清理）。
func testConfig(t *testing.T, maxConns int) config.DatabaseConfig {
	t.Helper()
	return config.DatabaseConfig{
		Driver:        "sqlite",
		Path:          filepath.Join(t.TempDir(), "test.db"),
		MaxOpenConns:  maxConns,
		MaxIdleConns:  maxConns,
		BusyTimeoutMS: 3000,
	}
}

func openStore(t *testing.T, cfg config.DatabaseConfig) *store.Store {
	t.Helper()
	s, err := store.Open(cfg)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// newTestStoreN 返回一个已迁移到最新版本、连接池上限为 maxConns 的库。
// 并发测试用 >1 的连接数，才能真正压到 UNIQUE 约束与事务边界上。
func newTestStoreN(t *testing.T, maxConns int) *store.Store {
	t.Helper()
	s := openStore(t, testConfig(t, maxConns))
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate 失败: %v", err)
	}
	return s
}

// newTestStore 返回一个已迁移到最新版本的单连接库（与生产配置一致）。
func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	return newTestStoreN(t, 1)
}

func TestOpen_DoesNotCreateTables(t *testing.T) {
	s := openStore(t, testConfig(t, 1))
	ctx := context.Background()

	ver, err := s.MigrationVersion(ctx)
	if err != nil {
		t.Fatalf("MigrationVersion 失败: %v", err)
	}
	if ver != 0 {
		t.Fatalf("新建库的 user_version = %d，期望 0", ver)
	}

	var n int
	if err := s.DB().QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`,
	).Scan(&n); err != nil {
		t.Fatalf("统计表数量失败: %v", err)
	}
	if n != 0 {
		t.Fatalf("Open 不应该建表，但发现 %d 张业务表", n)
	}
}

func TestMigrate_CreatesAllTables(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	ver, err := s.MigrationVersion(ctx)
	if err != nil {
		t.Fatalf("MigrationVersion 失败: %v", err)
	}
	if ver != 1 {
		t.Fatalf("迁移后 user_version = %d，期望 1", ver)
	}

	rows, err := s.DB().QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatalf("查询表清单失败: %v", err)
	}
	defer rows.Close()

	var got []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("扫描表名失败: %v", err)
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历表清单失败: %v", err)
	}

	want := []string{
		"conversations", "events", "groups", "llm_requests", "messages", "turns", "users",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("表清单 = %v，期望 %v", got, want)
	}
}

func TestMigrate_Idempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("第二次 Migrate 不应报错: %v", err)
	}

	ver, err := s.MigrationVersion(ctx)
	if err != nil {
		t.Fatalf("MigrationVersion 失败: %v", err)
	}
	if ver != 1 {
		t.Fatalf("重复迁移后 user_version = %d，期望仍为 1", ver)
	}
}

func TestLatestMigrationVersion(t *testing.T) {
	s := openStore(t, testConfig(t, 1))

	latest, err := s.LatestMigrationVersion()
	if err != nil {
		t.Fatalf("LatestMigrationVersion 失败: %v", err)
	}
	if latest != 1 {
		t.Fatalf("内嵌最高版本 = %d，期望 1", latest)
	}
}

// 迁移提交后版本号必须真正落盘：关闭再重开同一个库文件仍应看到 user_version=1。
// 这条测试守住「PRAGMA 在事务内执行」在驱动层确实生效（而不仅是同连接可见）。
func TestMigrate_PersistsAcrossReopen(t *testing.T) {
	cfg := testConfig(t, 1)
	ctx := context.Background()

	s := openStore(t, cfg)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("首次 Migrate 失败: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("关闭数据库失败: %v", err)
	}

	reopened := openStore(t, cfg)
	ver, err := reopened.MigrationVersion(ctx)
	if err != nil {
		t.Fatalf("重开后读取版本失败: %v", err)
	}
	if ver != 1 {
		t.Fatalf("重开后 user_version = %d，期望 1（版本号没有落盘）", ver)
	}
	if err := reopened.Migrate(ctx); err != nil {
		t.Fatalf("重开后重复 Migrate 不应报错: %v", err)
	}
}

func TestMigrate_RefusesNewerSchema(t *testing.T) {
	s := openStore(t, testConfig(t, 1))
	ctx := context.Background()

	if _, err := s.DB().ExecContext(ctx, "PRAGMA user_version = 999"); err != nil {
		t.Fatalf("写入 user_version 失败: %v", err)
	}
	if err := s.Migrate(ctx); err == nil {
		t.Fatal("库版本高于二进制支持的版本时，Migrate 应当报错")
	}
}

// PRAGMA 是 per-connection 的：必须走 DSN，否则池里其它连接不会生效（03 §4.6）。
func TestOpen_PragmasAppliedPerConnection(t *testing.T) {
	s := openStore(t, testConfig(t, 2))
	ctx := context.Background()

	conn1, err := s.DB().Conn(ctx)
	if err != nil {
		t.Fatalf("获取第一条连接失败: %v", err)
	}
	defer conn1.Close()

	conn2, err := s.DB().Conn(ctx)
	if err != nil {
		t.Fatalf("获取第二条连接失败: %v", err)
	}
	defer conn2.Close()

	for i, c := range []*sql.Conn{conn1, conn2} {
		var mode string
		if err := c.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
			t.Fatalf("第 %d 条连接读取 journal_mode 失败: %v", i+1, err)
		}
		if !strings.EqualFold(mode, "wal") {
			t.Fatalf("第 %d 条连接 journal_mode = %q，期望 wal", i+1, mode)
		}

		var fk int
		if err := c.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
			t.Fatalf("第 %d 条连接读取 foreign_keys 失败: %v", i+1, err)
		}
		if fk != 1 {
			t.Fatalf("第 %d 条连接 foreign_keys = %d，期望 1", i+1, fk)
		}
	}
}

func TestMigrate_ForeignKeysEnforced(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, err := s.DB().ExecContext(ctx, `
		INSERT INTO messages (turn_id, seq_in_turn, role, content, created_at)
		VALUES (?, 0, 'user', 'hi', ?)`, 9999, time.Now().UnixMilli())
	if err == nil {
		t.Fatal("插入指向不存在轮次的消息本应触发外键错误，但成功了：外键约束未生效")
	}
}
