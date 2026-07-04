// Package oracle Oracle 连接封装（对齐 tcg-ucs-fe 的 pkg/oracle 角色）。
//
// 驱动选择：这里用纯 Go 的 github.com/sijms/go-ora/v2，无需 CGO 和
// Oracle Instant Client，本地开发零依赖；tcg-ucs-fe 生产用的是 godror
// （ODPI-C，需要 Instant Client），两者都是 database/sql 驱动，
// 如需切换只改这里的 import 和 sql.Open 的驱动名即可，仓储层不动。
package oracle

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/sijms/go-ora/v2" // 注册 "oracle" 驱动
)

// Open 建立连接池并做一次连通性探测。
// dsn 形如 oracle://user:pass@host:1521/service_name
func Open(dsn string) (*sql.DB, error) {
	db, err := sql.Open("oracle", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开 Oracle 连接: %w", err)
	}
	db.SetMaxOpenConns(5) // 只有规则轮询在用，小池足够
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(30 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("Oracle 连通性探测失败: %w", err)
	}
	return db, nil
}
