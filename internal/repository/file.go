package repository

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"tcg-ai-engine/internal/model"
)

// FileRepository 从目录加载所有 *.grl 文件（默认数据源）。
// 文件名前缀数字（010_、020_...）仅用于人眼排序；
// 规则的实际执行顺序只由 salience 决定，与文件顺序无关。
type FileRepository struct {
	dir string
}

func NewFileRepository(dir string) *FileRepository {
	return &FileRepository{dir: dir}
}

func (r *FileRepository) Load(_ context.Context) ([]model.Rule, error) {
	paths, err := filepath.Glob(filepath.Join(r.dir, "*.grl"))
	if err != nil {
		return nil, fmt.Errorf("扫描规则目录 %s: %w", r.dir, err)
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("规则目录 %s 下没有 *.grl 文件", r.dir)
	}
	sort.Strings(paths) // Glob 结果顺序与平台相关，显式排序保证指纹稳定

	rules := make([]model.Rule, 0, len(paths))
	for _, p := range paths {
		content, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("读取规则文件 %s: %w", p, err)
		}
		info, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("读取规则文件信息 %s: %w", p, err)
		}
		rules = append(rules, model.Rule{
			Name:      filepath.Base(p),
			Content:   string(content),
			UpdatedAt: info.ModTime(),
		})
	}
	return rules, nil
}

func (r *FileRepository) Source() string {
	return "file:" + r.dir
}
