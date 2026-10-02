// Package store 是 engine.Store / engine.Tx 的 PostgreSQL 16 实现。
//
// 并发与事务：
//   - 每次写请求一个数据库事务；同站日并发写入在事务内先对涉及的站
//     依次执行 pg_advisory_xact_lock（站代码经哈希成 bigint，锁顺序按
//     站代码升序），序列化同站写入，保证大序号最终胜出且地块只重算一次。
//   - 观测写入与地块重算、事件写入在同一事务内提交，崩溃整体回滚。
package store

import (
	"context"
	"embed"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Store 连接池 + 事务工厂，实现 engine.Store。
type Store struct {
	pool *pgxpool.Pool
}

// Tx 是事务句柄类型别名，供泛型 engine 实例化使用。
type Tx = *pgTx

// New 创建存储层。
func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Pool 暴露底层连接池（健康检查等用）。
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Close 关闭连接池。
func (s *Store) Close() { s.pool.Close() }

// Migrate 按顺序执行 migrations 目录下全部 SQL（IF NOT EXISTS，可重复执行）。
func (s *Store) Migrate(ctx context.Context) error {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		sqlBytes, err := migrationsFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return err
		}
		if _, err := s.pool.Exec(ctx, string(sqlBytes)); err != nil {
			return fmt.Errorf("迁移 %s 失败：%w", e.Name(), err)
		}
	}
	return nil
}

// Update 在一个可读写事务中执行 fn。
func (s *Store) Update(ctx context.Context, fn func(*pgTx) error) error {
	return s.run(ctx, true, fn)
}

// View 在一个只读事务中执行 fn。
func (s *Store) View(ctx context.Context, fn func(*pgTx) error) error {
	return s.run(ctx, false, fn)
}

func (s *Store) run(ctx context.Context, writable bool, fn func(*pgTx) error) error {
	opts := pgx.TxOptions{}
	if !writable {
		opts.AccessMode = pgx.ReadOnly
	}
	tx, err := s.pool.BeginTx(ctx, opts)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	if err := fn(&pgTx{tx: tx, ctx: ctx}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	committed = true
	return nil
}
