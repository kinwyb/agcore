package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"slices"
	"sync"

	"github.com/kinwyb/agcore/types"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
)

// Registry 工具注册表
type Registry struct {
	tools           map[string]types.Tool
	mu              sync.RWMutex
	needApproveTool []string
	prompters       map[string]types.IApprovalPrompt // 按工具名称注册的审批提示器
}

// NewRegistry 创建工具注册表
func NewRegistry() *Registry {
	r := &Registry{
		tools:     make(map[string]types.Tool),
		prompters: make(map[string]types.IApprovalPrompt),
	}
	r.initDefaultTool()
	return r
}

// 初始化默认工具
func (r *Registry) initDefaultTool() {
	// 获取当前时间
	r.Register(NewGetCurrentTimeTool())
	// 注册 Python 工具（Docker 沙盒执行）
	if _, err := exec.LookPath("docker"); err == nil {
		pythonTool := NewPythonTool(30)
		if err := r.Register(pythonTool, false); err != nil {
			slog.Warn("Failed to register python tool", "error", err)
		} else {
			slog.Debug("Python tool registered")
		}
	} else {
		slog.Debug("Docker not available, skipping python tool registration")
	}
}

// Register 注册工具
func (r *Registry) Register(tool types.Tool, approve ...bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := tool.Name()
	if _, ok := r.tools[name]; ok {
		return fmt.Errorf("tool %s already registered", name)
	}

	r.tools[name] = tool
	if len(approve) > 0 && approve[0] {
		r.needApproveTool = append(r.needApproveTool, name)
	}
	slog.Info("Tool registered", "tool", name)
	return nil
}

func (r *Registry) NeedApprove(toolName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if slices.Contains(r.needApproveTool, toolName) {
		return
	}
	r.needApproveTool = append(r.needApproveTool, toolName)
}

// GetTools 获取所有执行工具
func (r *Registry) GetTools() ([]tool.BaseTool, error) {
	var tools []tool.BaseTool
	for _, tool := range r.tools {
		nt, err := newInvokeableTool(tool)
		if err != nil {
			return nil, err
		}
		tools = append(tools, nt)
	}
	return tools, nil
}

// ToolCount 工具数量统计
func (r *Registry) ToolCount() int {
	return len(r.tools)
}

// SetAllowedTools 设置允许的工具白名单
// 如果 whitelist 为空，表示所有工具都可用
func (r *Registry) SetAllowedTools(whitelist []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(whitelist) == 0 {
		return // 空白名单表示所有工具可用
	}
	// 删除不在白名单中的工具
	for name := range r.tools {
		if !slices.Contains(whitelist, name) {
			delete(r.tools, name)
			slog.Debug("Tool removed (not in whitelist)", "tool", name)
		}
	}
}

// SetToolsApproval 设置需要审批的工具列表
func (r *Registry) SetToolsApproval(approvalList []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.needApproveTool = approvalList
	slog.Debug("Tools approval list set", "tools", approvalList)
}

// GetToolNames 获取所有已注册的工具名称
func (r *Registry) GetToolNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	return names
}

// RegisterApprovalPrompter 注册工具的审批提示器
// 允许为工具单独设置审批提示，即使工具本身未实现 IApprovalPrompt 接口
func (r *Registry) RegisterApprovalPrompter(toolName string, prompter types.IApprovalPrompt) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prompters[toolName] = prompter
}

// getApprovalPrompt 获取工具的审批提示
// 优先使用注册的 ApprovalPrompter，如果没有则检查工具是否实现了 ApprovalPrompter 接口
func (r *Registry) getApprovalPrompt(toolName, argsJSON string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// 优先检查注册的 ApprovalPrompter
	if prompter, ok := r.prompters[toolName]; ok {
		return prompter.ApprovalPrompt(argsJSON)
	}

	// 然后检查工具本身是否实现了 ApprovalPrompter
	tool, ok := r.tools[toolName]
	if !ok {
		return ""
	}

	if prompter, ok := tool.(types.IApprovalPrompt); ok {
		return prompter.ApprovalPrompt(argsJSON)
	}
	return ""
}

type invokeableTool struct {
	tool types.Tool
}

// NewInvokeableTool 创建一个执行工具
func newInvokeableTool(tool types.Tool) (tool.InvokableTool, error) {
	if tool == nil {
		return nil, errors.New("tool is nil")
	}
	return &invokeableTool{tool: tool}, nil
}

func (i invokeableTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	param, _ := i.mapToSchema(i.tool.Parameters())
	toolInfo := &schema.ToolInfo{
		Name:        i.tool.Name(),
		Desc:        i.tool.Description(),
		ParamsOneOf: schema.NewParamsOneOfByJSONSchema(param),
	}
	return toolInfo, nil
}

func (i invokeableTool) Parameters() map[string]any {
	return i.tool.Parameters()
}

func (i invokeableTool) mapToSchema(data map[string]any) (*jsonschema.Schema, error) {
	if data == nil {
		return nil, errors.New("nil map")
	}
	jsonBytes, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("marshal map failed: %w", err)
	}
	var js jsonschema.Schema
	if err := json.Unmarshal(jsonBytes, &js); err != nil {
		return nil, fmt.Errorf("unmarshal to jsonschema.Schema failed: %w", err)
	}
	return &js, nil
}

func (i invokeableTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	param := make(map[string]any)
	err := json.Unmarshal([]byte(argumentsInJSON), &param)
	if err != nil {
		return "", err
	}
	return i.tool.Execute(ctx, param)
}
