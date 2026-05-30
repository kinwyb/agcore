package agcore

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/kinwyb/agcore/tools"

	"github.com/cloudwego/eino/components/tool"
	"github.com/eino-contrib/jsonschema"
)

// loadMCPTools 加载 MCP 工具到注册表，返回加载器实例
func loadMCPTools(ctx context.Context, register *tools.Registry, mcpConfigs []tools.MCPConfig) (*tools.MCPLoader, error) {
	if len(mcpConfigs) == 0 || register == nil {
		return nil, nil
	}

	loader := tools.NewMCPLoader()
	mcpTools, err := loader.LoadTools(ctx, mcpConfigs)
	if err != nil {
		return loader, fmt.Errorf("failed to load MCP tools: %w", err)
	}

	for _, t := range mcpTools {
		info, err := t.Info(ctx)
		if err != nil {
			slog.Warn("Failed to get MCP tool info", "error", err)
			continue
		}
		// 将 MCP 工具包装为 invokeableTool
		// 从 ParamsOneOf 获取参数 schema
		params := make(map[string]any)
		if info.ParamsOneOf != nil {
			js, err := info.ParamsOneOf.ToJSONSchema()
			if err == nil && js != nil {
				// 将 jsonschema.Schema 转换为 map
				params = schemaToMap(js)
			}
		}
		wrappedTool := &mcpToolWrapper{baseTool: t, name: info.Name, desc: info.Desc, params: params}
		if err := register.Register(wrappedTool); err != nil {
			slog.Warn("Failed to register MCP tool", "name", info.Name, "error", err)
		}
	}

	return loader, nil
}

// schemaToMap 将 jsonschema.Schema 转换为 map[string]any
func schemaToMap(js *jsonschema.Schema) map[string]any {
	if js == nil {
		return nil
	}
	// 使用 json 序列化再反序列化来转换
	data, err := json.Marshal(js)
	if err != nil {
		return nil
	}
	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil
	}
	return result
}

// mcpToolWrapper 包装 MCP 工具以实现 Tool 接口
type mcpToolWrapper struct {
	baseTool tool.BaseTool
	name     string
	desc     string
	params   map[string]interface{}
}

func (w *mcpToolWrapper) Name() string {
	return w.name
}

func (w *mcpToolWrapper) Description() string {
	return w.desc
}

func (w *mcpToolWrapper) Parameters() map[string]any {
	return w.params
}

func (w *mcpToolWrapper) Execute(ctx context.Context, args map[string]any) (string, error) {
	// MCP 工具通过 InvokableRun 执行
	invokable, ok := w.baseTool.(tool.InvokableTool)
	if !ok {
		return "", fmt.Errorf("MCP tool %s is not invokable", w.name)
	}

	argsJSON, err := jsonArgs(args)
	if err != nil {
		return "", err
	}

	return invokable.InvokableRun(ctx, argsJSON)
}

func jsonArgs(args map[string]any) (string, error) {
	if len(args) == 0 {
		return "{}", nil
	}
	data, err := json.Marshal(args)
	if err != nil {
		return "", fmt.Errorf("failed to marshal args: %w", err)
	}
	return string(data), nil
}
