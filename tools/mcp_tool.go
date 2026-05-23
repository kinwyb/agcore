package tools

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	mcpp "github.com/cloudwego/eino-ext/components/tool/mcp"
	"github.com/cloudwego/eino/components/tool"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

// MCPLoader MCP 工具加载器
type MCPLoader struct {
	clients map[string]*client.Client // MCP 客户端缓存
	mu      sync.RWMutex
}

// NewMCPLoader 创建 MCP 加载器
func NewMCPLoader() *MCPLoader {
	return &MCPLoader{
		clients: make(map[string]*client.Client),
	}
}

// LoadTools 从 MCP 配置加载工具
func (l *MCPLoader) LoadTools(ctx context.Context, configs []MCPConfig) ([]tool.BaseTool, error) {
	if len(configs) == 0 {
		return nil, nil
	}

	var allTools []tool.BaseTool
	for _, cfg := range configs {
		if !cfg.Enabled {
			slog.Debug("MCP config disabled", "name", cfg.Name)
			continue
		}

		tools, err := l.loadToolsFromConfig(ctx, cfg)
		if err != nil {
			slog.Error("Failed to load MCP tools", "name", cfg.Name, "error", err)
			continue
		}

		allTools = append(allTools, tools...)
		slog.Info("MCP tools loaded", "name", cfg.Name, "count", len(tools))
	}

	return allTools, nil
}

// loadToolsFromConfig 从单个 MCP 配置加载工具
func (l *MCPLoader) loadToolsFromConfig(ctx context.Context, cfg MCPConfig) ([]tool.BaseTool, error) {
	// 设置默认超时
	timeout := cfg.InitTimeout
	if timeout <= 0 {
		timeout = 30
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	// 创建或获取客户端
	cli, err := l.getOrCreateClient(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create MCP client: %w", err)
	}

	// 获取工具
	mcpTools, err := mcpp.GetTools(ctx, &mcpp.Config{
		Cli:          cli,
		ToolNameList: cfg.Tools,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get MCP tools: %w", err)
	}

	return mcpTools, nil
}

// getOrCreateClient 获取或创建 MCP 客户端
func (l *MCPLoader) getOrCreateClient(ctx context.Context, cfg MCPConfig) (*client.Client, error) {
	l.mu.RLock()
	if cli, ok := l.clients[cfg.Name]; ok {
		l.mu.RUnlock()
		return cli, nil
	}
	l.mu.RUnlock()

	l.mu.Lock()
	defer l.mu.Unlock()

	// 再次检查，防止并发创建
	if cli, ok := l.clients[cfg.Name]; ok {
		return cli, nil
	}

	cli, err := l.createClient(ctx, cfg)
	if err != nil {
		return nil, err
	}

	l.clients[cfg.Name] = cli
	return cli, nil
}

// createClient 创建 MCP 客户端
func (l *MCPLoader) createClient(ctx context.Context, cfg MCPConfig) (*client.Client, error) {
	var cli *client.Client
	var err error

	switch cfg.Type {
	case "sse":
		if cfg.URL == "" {
			return nil, fmt.Errorf("MCP SSE config missing URL")
		}
		cli, err = client.NewSSEMCPClient(cfg.URL)
		if err != nil {
			return nil, fmt.Errorf("failed to create SSE client: %w", err)
		}

	case "stdio":
		if cfg.Command == "" {
			return nil, fmt.Errorf("MCP stdio config missing command")
		}
		// 转换环境变量
		env := make([]string, 0, len(cfg.Env))
		for k, v := range cfg.Env {
			env = append(env, fmt.Sprintf("%s=%s", k, v))
		}
		cli, err = client.NewStdioMCPClient(cfg.Command, env, cfg.Args...)
		if err != nil {
			return nil, fmt.Errorf("failed to create stdio client: %w", err)
		}

	default:
		return nil, fmt.Errorf("unknown MCP type: %s (use 'sse' or 'stdio')", cfg.Type)
	}

	// 启动客户端
	if err := cli.Start(ctx); err != nil {
		return nil, fmt.Errorf("failed to start MCP client: %w", err)
	}

	// 初始化客户端
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{
		Name:    "kanflux",
		Version: "1.0.0",
	}

	if _, err := cli.Initialize(ctx, initReq); err != nil {
		cli.Close()
		return nil, fmt.Errorf("failed to initialize MCP client: %w", err)
	}

	slog.Info("MCP client initialized", "name", cfg.Name, "type", cfg.Type)
	return cli, nil
}

// Close 关闭所有 MCP 客户端
func (l *MCPLoader) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	var errs []error
	for name, cli := range l.clients {
		if err := cli.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close MCP client %s: %w", name, err))
		}
		slog.Debug("MCP client closed", "name", name)
	}

	l.clients = make(map[string]*client.Client)

	if len(errs) > 0 {
		return fmt.Errorf("errors closing MCP clients: %v", errs)
	}
	return nil
}

// MCPConfig MCP 工具配置（从 config 包导入）
type MCPConfig = struct {
	Name        string            `json:"name"`
	Type        string            `json:"type"`
	URL         string            `json:"url"`
	Command     string            `json:"command"`
	Args        []string          `json:"args"`
	Env         map[string]string `json:"env"`
	Tools       []string          `json:"tools"`
	Enabled     bool              `json:"enabled"`
	InitTimeout int               `json:"init_timeout"`
}