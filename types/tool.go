package types

import "context"

// Tool 工具接口
type Tool interface {
	// Name 工具名称
	Name() string

	// Description 工具描述
	Description() string

	// Parameters JSON Schema 参数定义
	Parameters() map[string]any

	// Execute 执行工具
	Execute(ctx context.Context, params map[string]any) (string, error)
}

// BaseTool 基础工具
type BaseTool struct {
	name        string
	description string
	parameters  map[string]any
	executeFunc func(ctx context.Context, params map[string]any) (string, error)
}

// NewBaseTool 创建基础工具
func NewBaseTool(name, description string, parameters map[string]any, executeFunc func(ctx context.Context, params map[string]any) (string, error)) *BaseTool {
	return &BaseTool{
		name:        name,
		description: description,
		parameters:  parameters,
		executeFunc: executeFunc,
	}
}

// Name 返回工具名称
func (t *BaseTool) Name() string {
	return t.name
}

// Description 返回工具描述
func (t *BaseTool) Description() string {
	return t.description
}

// Parameters 返回参数定义
func (t *BaseTool) Parameters() map[string]interface{} {
	return t.parameters
}

// Execute 执行工具
func (t *BaseTool) Execute(ctx context.Context, params map[string]any) (string, error) {
	return t.executeFunc(ctx, params)
}
