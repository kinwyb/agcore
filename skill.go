package agcore

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/adk/middlewares/skill"
)

// multiSkillBackend 聚合多个 skill Backend，支持从多个目录加载 skill
type multiSkillBackend struct {
	backends []skill.Backend
}

// NewMultiSkillBackend 创建聚合多个 Backend 的复合 Backend
func NewMultiSkillBackend(backends ...skill.Backend) skill.Backend {
	return &multiSkillBackend{backends: backends}
}

// List 列出所有 Backend 中的 skill 元数据
func (m *multiSkillBackend) List(ctx context.Context) ([]skill.FrontMatter, error) {
	var matters []skill.FrontMatter
	for _, b := range m.backends {
		list, err := b.List(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list skills from backend: %w", err)
		}
		matters = append(matters, list...)
	}
	return matters, nil
}

// Get 从所有 Backend 中查找指定名称的 skill
func (m *multiSkillBackend) Get(ctx context.Context, name string) (skill.Skill, error) {
	for _, b := range m.backends {
		s, err := b.Get(ctx, name)
		if err == nil {
			return s, nil
		}
		// 继续尝试下一个 Backend
	}
	return skill.Skill{}, fmt.Errorf("skill not found: %s", name)
}

// NewSkillBackends 从多个目录创建 skill Backend 列表
func NewSkillBackends(ctx context.Context, fsBackend filesystem.Backend, skillDirs []string) ([]skill.Backend, error) {
	if len(skillDirs) == 0 {
		return nil, nil
	}

	backends := make([]skill.Backend, 0, len(skillDirs))
	for _, dir := range skillDirs {
		if dir == "" {
			continue
		}
		b, err := skill.NewBackendFromFilesystem(ctx, &skill.BackendFromFilesystemConfig{
			Backend: fsBackend,
			BaseDir: dir,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create skill backend for dir %s: %w", dir, err)
		}
		backends = append(backends, b)
	}
	return backends, nil
}
