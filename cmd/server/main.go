// Command server 启动玉米积温预测后端。
//
// 启动流程：连 PostgreSQL → 执行迁移 → 对全部地块全量对齐（重启续跑）
// → 起 HTTP 服务。
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"agristation/internal/api"
	"agristation/internal/engine"
	"agristation/internal/store"

	"github.com/jackc/pgx/v5/pgxpool"
)

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	dsn := getenv("DATABASE_URL",
		"postgres://agri:agri@localhost:5432/agristation?sslmode=disable")
	addr := getenv("HTTP_ADDR", ":8080")

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatalf("连接数据库失败：%v", err)
	}
	defer pool.Close()

	pg := waitForDB(ctx, pool)
	st := store.New(pg)
	if err := st.Migrate(ctx); err != nil {
		log.Fatalf("数据库迁移失败：%v", err)
	}

	svc := engine.NewService[store.Tx](st)
	if err := svc.Recover(ctx); err != nil {
		log.Printf("警告：启动全量对齐失败：%v（将继续启动，可在数据写入时自动修复）", err)
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           api.NewServer[store.Tx](svc).Router(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("玉米积温服务监听 %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP 服务退出：%v", err)
		}
	}()

	// 跨日滚动：每小时检查一次，日期变化时全量对齐预测基准。
	rolloverCtx, stopRollover := context.WithCancel(context.Background())
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := svc.Rollover(rolloverCtx); err != nil {
					log.Printf("跨日滚动失败：%v", err)
				}
			case <-rolloverCtx.Done():
				return
			}
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("收到退出信号，优雅关闭中…")
	stopRollover()
	shCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shCtx); err != nil {
		log.Printf("关闭超时：%v", err)
	}
}

// waitForDB 等待数据库就绪（compose 中应用可能早于数据库启动）。
func waitForDB(ctx context.Context, pool *pgxpool.Pool) *pgxpool.Pool {
	deadline := time.Now().Add(60 * time.Second)
	for {
		if err := pool.Ping(ctx); err == nil {
			return pool
		} else if time.Now().After(deadline) {
			log.Fatalf("数据库在 60s 内未就绪：%v", err)
		}
		time.Sleep(time.Second)
	}
}
