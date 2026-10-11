package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"qq-bot/internal/config"
	"qq-bot/internal/store"
	"testing"
)

type Result_test struct {
	id   int
	name string
}

func TestStore(t *testing.T) {
	ctx := context.Background()

	tempDB := filepath.Join(t.TempDir(), "test_schema.sql")

	cfg := config.DatabaseConfig{
		Driver:        "sqlite",
		Path:          tempDB,
		MaxOpenConns:  1,
		MaxIdleConns:  1,
		BusyTimeoutMS: 3000,
		InitSchema:    true,
	}

	s, err := store.Open(cfg)
	if err != nil {
		t.Fatalf("Open 失败：%v", err)
	}
	defer s.Close()

	err = s.InitSchema(ctx)
	if err != nil {
		t.Fatalf("初始化建表失败：%v", err)
	}

	query := `insert into test_table (id, name) values (?, ?)`
	id := 123
	name := "makabaka"
	res, err := s.DB().ExecContext(ctx, query, id, name)
	if err != nil {
		t.Fatalf("插入数据失败：%v", err)
	}
	fmt.Print(res)

	query = `select * from test_table`
	rows, err := s.DB().Query(query)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			rows = nil
		}
	}
	for rows.Next() {
		var c Result_test
		if err := rows.Scan(&c.id, &c.name); err != nil {
			t.Fatalf("查询失败:%v", err)
		}
		fmt.Printf("id: %d, name: %s", c.id, c.name)
	}
}
