package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "embed"

	_ "modernc.org/sqlite"

	"qq-bot/internal/config"
)

// 采用 dmbed 注入 sql 文件，需要改动下方的文件名

//go:embed migrations/0001_init.sql
var schemaSQL string

type Store struct {
	db  *sql.DB
	cfg config.DatabaseConfig
}

// 初始化，并获取 DB 实例
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

	s := &Store{
		db:  db,
		cfg: cfg,
	}

	if cfg.InitSchema {
		if err := s.InitSchema(context.Background()); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("自动初始化表结构失败：%w", err)
		}
	}

	return s, nil
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

// 执行数据库初始化建表 sql
func (s *Store) InitSchema(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("执行 schema.sql 失败: %v", err)
	}
	return nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) DB() *sql.DB {
	return s.db
}
