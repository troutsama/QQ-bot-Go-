package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
)

// 迁移文件随二进制一起编译（03 §4.5）。
// 这里用通配符而不是单个文件名：新增 0002_xxx.sql 时不需要改 Go 代码。
//
//go:embed migrations/*.sql
var migrationFS embed.FS

// migration 是一个迁移单元：文件名前缀的数字即版本号。
type migration struct {
	version int
	name    string
	sql     string
}

// MigrationVersion 返回数据库的当前版本（PRAGMA user_version，空库为 0）。
//
// 它是「-migrate-status」与 admin 健康检查的数据来源，只读、无副作用。
func (s *Store) MigrationVersion(ctx context.Context) (int, error) {
	var v int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return 0, fmt.Errorf("读取 user_version 失败: %w", err)
	}
	return v, nil
}

// LatestMigrationVersion 返回二进制内嵌迁移的最高版本，即数据库「应当」到达的版本。
// 与 MigrationVersion（数据库当前版本）配对，供 -migrate-status 与运维排障使用。
func (s *Store) LatestMigrationVersion() (int, error) {
	files, err := loadMigrations()
	if err != nil {
		return 0, err
	}
	return files[len(files)-1].version, nil
}

// Migrate 把数据库推进到二进制内嵌的最高迁移版本。
//
// 规则（03 §4.5）：
//   - 只执行 version > 当前 user_version 的迁移，按版本号升序；
//   - 每个迁移在**单独事务**内执行，SQL 与 user_version 的写入同属一个事务
//     （SQLite 的 PRAGMA user_version 是事务性的，崩溃时一起回滚；
//     若把它放到事务外，会出现「表建好了但版本号没写」→ 下次重跑必撞 table already exists）；
//   - 任何一步失败立即返回错误，由调用方让进程以非 0 退出，绝不静默继续。
func (s *Store) Migrate(ctx context.Context) error {
	current, err := s.MigrationVersion(ctx)
	if err != nil {
		return err
	}

	files, err := loadMigrations()
	if err != nil {
		return err
	}
	target := files[len(files)-1].version
	if current > target {
		return fmt.Errorf(
			"数据库 user_version=%d 高于本程序支持的最高版本 %d：请升级程序，或使用与库匹配的旧二进制",
			current, target)
	}

	for _, m := range files {
		if m.version <= current {
			continue
		}
		if err := s.applyMigration(ctx, m); err != nil {
			return fmt.Errorf("迁移 %s 失败（user_version 仍为 %d）: %w", m.name, current, err)
		}
		current = m.version
	}
	return nil
}

// applyMigration 在单个事务内执行一个迁移，并把版本号推进到该迁移的版本。
func (s *Store) applyMigration(ctx context.Context, m migration) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开启事务失败: %w", err)
	}
	// Commit 成功后再 Rollback 会返回 sql.ErrTxDone，按惯用法直接忽略（06 §9.3.4）。
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return fmt.Errorf("执行 SQL 失败: %w", err)
	}

	// PRAGMA 不支持占位符参数，只能拼字符串；version 来自编译期嵌入的文件名，
	// 是程序自己解析出的正整数，不存在注入面。
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", m.version)); err != nil {
		return fmt.Errorf("写入 user_version=%d 失败: %w", m.version, err)
	}

	return tx.Commit()
}

// loadMigrations 读取全部内嵌迁移，按版本号升序返回，并校验文件名与版本号唯一性。
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("读取内嵌迁移目录失败: %w", err)
	}

	out := make([]migration, 0, len(entries))
	seen := make(map[int]string, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		version, err := parseMigrationVersion(e.Name())
		if err != nil {
			return nil, err
		}
		if prev, dup := seen[version]; dup {
			return nil, fmt.Errorf("迁移版本号重复：%s 与 %s 都是版本 %d", prev, e.Name(), version)
		}
		seen[version] = e.Name()

		body, err := migrationFS.ReadFile(path.Join("migrations", e.Name()))
		if err != nil {
			return nil, fmt.Errorf("读取迁移文件 %s 失败: %w", e.Name(), err)
		}
		out = append(out, migration{version: version, name: e.Name(), sql: string(body)})
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("内嵌迁移目录中没有 .sql 文件")
	}

	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	if err := validateMigrationSequence(out); err != nil {
		return nil, err
	}
	return out, nil
}

// validateMigrationSequence 要求迁移版本从 1 开始且连续。
//
// 只查重还不够：如果 0001_init.sql 被误删、目录里只剩 0002，Migrate 会把空库
// 直接推进到 user_version=2，之后所有业务查询都在缺表的情况下失败，
// 而版本号看起来却是“最新”的。这里把这种配置错误挡在迁移开始之前。
func validateMigrationSequence(files []migration) error {
	for i, m := range files {
		want := i + 1
		if m.version != want {
			return fmt.Errorf(
				"迁移版本不连续：%s 的版本是 %d，期望 %d（migrations 目录可能缺文件）",
				m.name, m.version, want)
		}
	}
	return nil
}

// parseMigrationVersion 从 "0002_users.sql" 中解析出版本号 2。
func parseMigrationVersion(name string) (int, error) {
	prefix, _, ok := strings.Cut(strings.TrimSuffix(name, ".sql"), "_")
	if !ok {
		return 0, fmt.Errorf("迁移文件名 %q 不符合 NNNN_描述.sql 的约定", name)
	}
	v, err := strconv.Atoi(prefix)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("迁移文件名 %q 的版本号不是正整数", name)
	}
	return v, nil
}
