package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"

	"qq-bot/internal/config"
)

// Store 是数据持久化适配器。所有 SQL 都封装在本包内，不对上层暴露。
//
// 它**不负责建表**：打开连接与推进 schema 是两步（03 §5.6），
// 建表/升级由调用方显式调用 Migrate 完成。
type Store struct {
	db  *sql.DB
	cfg config.DatabaseConfig
}

// Open 按配置打开数据库并验证连通性；它**不建表**，迁移由 Migrate 负责。
func Open(cfg config.DatabaseConfig) (*Store, error) {
	var (
		db  *sql.DB
		err error
	)
	switch cfg.Driver {
	case "sqlite":
		db, err = openSQLite(cfg)
	case "mysql":
		// TODO 待后续实现 MySQL 存储部分，可用 Docker 测试
		return nil, fmt.Errorf("暂不支持 MySQL 驱动")
	default:
		return nil, fmt.Errorf("不支持的数据库驱动：%q", cfg.Driver)
	}

	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("数据库 Ping 失败：%w", err)
	}

	return &Store{
		db:  db,
		cfg: cfg,
	}, nil
}

func openSQLite(cfg config.DatabaseConfig) (*sql.DB, error) {
	var dsn string

	if cfg.DSN != "" {
		dsn = cfg.DSN
	} else {
		dir := filepath.Dir(cfg.Path)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("创建数据库目录失败 %s 失败: %w", dir, err)
		}

		timeout := cfg.BusyTimeoutMS
		if timeout <= 0 {
			timeout = 5000
		}
		dsn = fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(%d)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)",
			cfg.Path, timeout)
	}
	return sql.Open("sqlite", dsn)
}

// Ping 用于启动自检与 admin 健康检查（03 §5.4）。
func (s *Store) Ping(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return fmt.Errorf("数据库不可用: %w", err)
	}
	return nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) DB() *sql.DB {
	return s.db
}
