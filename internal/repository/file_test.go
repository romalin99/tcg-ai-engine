package repository

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFileRepositoryLoad(t *testing.T) {
	dir := t.TempDir()
	// 乱序创建，验证 Load 按文件名排序
	files := map[string]string{
		"020_b.grl":  "rule B \"b\" { when 1 == 1 then Retract(\"B\"); }",
		"010_a.grl":  "rule A \"a\" { when 1 == 1 then Retract(\"A\"); }",
		"ignore.txt": "not a rule",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	repo := NewFileRepository(dir)
	rules, err := repo.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 2 {
		t.Fatalf("应只加载 *.grl，got %d 个", len(rules))
	}
	if rules[0].Name != "010_a.grl" || rules[1].Name != "020_b.grl" {
		t.Fatalf("应按文件名排序: %s, %s", rules[0].Name, rules[1].Name)
	}
	if rules[0].UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt 应取文件 mtime")
	}
}

func TestFileRepositoryEmptyDir(t *testing.T) {
	repo := NewFileRepository(t.TempDir())
	if _, err := repo.Load(context.Background()); err == nil {
		t.Fatal("空目录应报错而不是静默加载 0 条规则")
	}
}
