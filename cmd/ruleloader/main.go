// ruleloader 把本地 rules/*.grl 批量导入（MERGE）Oracle 规则表，
// 供 rules.source=oracle 模式使用：
//
//	go run ./cmd/ruleloader -dsn 'oracle://user:pass@host:1521/svc' -dir rules -table RISK_RULES
//
// 之后直接 UPDATE 表里的 GRL_CONTENT / ENABLED，服务会在下个轮询周期自动热加载。
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/romalin99/tcg-ai-engine/pkg/oracle"
)

func main() {
	dsn := flag.String("dsn", "", "Oracle 连接串：oracle://user:pass@host:1521/service_name")
	dir := flag.String("dir", "rules", "GRL 规则目录")
	table := flag.String("table", "RISK_RULES", "规则表名")
	flag.Parse()

	if *dsn == "" {
		fmt.Fprintln(os.Stderr, "必须提供 -dsn")
		os.Exit(1)
	}
	if err := run(*dsn, *dir, *table); err != nil {
		fmt.Fprintln(os.Stderr, "导入失败:", err)
		os.Exit(1)
	}
}

func run(dsn, dir, table string) error {
	paths, err := filepath.Glob(filepath.Join(dir, "*.grl"))
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return fmt.Errorf("目录 %s 下没有 *.grl 文件", dir)
	}
	sort.Strings(paths)

	db, err := oracle.Open(dsn)
	if err != nil {
		return err
	}
	defer db.Close()

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
