// ruleloader 把本地 rules/*.grl 批量导入（MERGE）Oracle 规则表，
// 供 rules.source=oracle 模式使用：
//
//	go run ./cmd/ruleloader -user scott -password tiger -connect host:1521/svc -dir rules -table RISK_RULES
//
// 之后直接 UPDATE 表里的 GRL_CONTENT / ENABLED，服务会在下个轮询周期自动热加载。
// 驱动与服务一致，用 godror（CGO/ODPI-C），运行环境需要 Oracle Client 库。
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	_ "github.com/godror/godror"
)

func main() {
	user := flag.String("user", "", "Oracle 用户名")
	password := flag.String("password", "", "Oracle 密码")
	connect := flag.String("connect", "", "连接串（EZConnect）：host:1521/service_name")
	options := flag.String("options", "", "附加 godror 连接参数（可选），如 stmtCacheSize=64")
	dir := flag.String("dir", "rules", "GRL 规则目录")
	table := flag.String("table", "RISK_RULES", "规则表名")
	flag.Parse()

	if *user == "" || *connect == "" {
		fmt.Fprintln(os.Stderr, "必须提供 -user 和 -connect（EZConnect：host:1521/service_name）")
		os.Exit(1)
	}
	if err := run(*user, *password, *connect, *options, *dir, *table); err != nil {
		fmt.Fprintln(os.Stderr, "导入失败:", err)
		os.Exit(1)
	}
}

func run(user, password, connect, options, dir, table string) error {
	paths, err := filepath.Glob(filepath.Join(dir, "*.grl"))
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return fmt.Errorf("目录 %s 下没有 *.grl 文件", dir)
	}
	sort.Strings(paths)

	// 连接串拼法与 pkg/oracle 的 Config.Init 一致
	dsn := fmt.Sprintf("user=%q password=%q connectString=%q %s", user, password, connect, options)
	db, err := sql.Open("godror", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		return fmt.Errorf("连接 Oracle 失败: %w", err)
	}

	// 以文件名为主键 MERGE：存在则更新内容并刷新时间戳，不存在则插入
	merge := fmt.Sprintf(`MERGE INTO %s t
USING (SELECT :1 AS RULE_NAME, :2 AS GRL_CONTENT FROM dual) s
ON (t.RULE_NAME = s.RULE_NAME)
WHEN MATCHED THEN UPDATE SET t.GRL_CONTENT = s.GRL_CONTENT, t.UPDATED_AT = SYSTIMESTAMP
WHEN NOT MATCHED THEN INSERT (RULE_NAME, GRL_CONTENT, ENABLED, UPDATED_AT)
VALUES (s.RULE_NAME, s.GRL_CONTENT, 1, SYSTIMESTAMP)`, table)

	for _, p := range paths {
		content, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		name := filepath.Base(p)
		if _, err := db.Exec(merge, name, string(content)); err != nil {
			return fmt.Errorf("导入 %s: %w", name, err)
		}
		fmt.Printf("已导入 %s（%d 字节）\n", name, len(content))
	}
	fmt.Printf("完成：%d 个规则文件已同步到 %s\n", len(paths), table)
	return nil
}
