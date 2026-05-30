package agcore

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/kinwyb/agcore/tools"
)

const (
	mcpPingTimeout        = 2 * time.Second  // MCP ping 超时
	mcpAvailCheckInterval = 15 * time.Second // 状态缓存有效期
)

// mcpAvailEntry 缓存条目
type mcpAvailEntry struct {
	avail   bool
	expires time.Time
}

// mcpAvailMiddleware 在执行 MCP 工具前检查对应 MCP 服务是否可用。
// 不可用时返回友好错误信息，避免模型收到晦涩的连接错误。
type mcpAvailMiddleware struct {
	*adk.BaseChatModelAgentMiddleware

	loader       *tools.MCPLoader  // MCP 加载器，提供 client 访问
	toolToServer map[string]string // tool name -> server name (BeforeAgent 中填充)
	cache        sync.Map          // server name -> mcpAvailEntry
	pingMu       sync.Mutex        // 保护同一 server 只并发一个 ping
}

// NewMCPAvailMiddleware 创建 MCP 可用性检查中间件。
// loader 为 nil 时中间件不做任何操作（透传）。
func NewMCPAvailMiddleware(loader *tools.MCPLoader) adk.ChatModelAgentMiddleware {
	return &mcpAvailMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		loader:                       loader,
		toolToServer:                 make(map[string]string),
	}
}

// BeforeAgent 填充工具到 MCP 服务的映射。
func (m *mcpAvailMiddleware) BeforeAgent(
	ctx context.Context,
	runCtx *adk.ChatModelAgentContext,
) (context.Context, *adk.ChatModelAgentContext, error) {
	if m.loader == nil {
		return ctx, runCtx, nil
	}

	m.toolToServer = m.loader.GetToolToServerMap()
	return ctx, runCtx, nil
}

// WrapInvokableToolCall 在调用前检查 MCP 服务可用性。
func (m *mcpAvailMiddleware) WrapInvokableToolCall(
	ctx context.Context,
	endpoint adk.InvokableToolCallEndpoint,
	tCtx *adk.ToolContext,
) (adk.InvokableToolCallEndpoint, error) {
	serverName := m.getServerForTool(tCtx.Name)
	if serverName == "" {
		// 不是 MCP 工具，直接放行
		return endpoint, nil
	}

	return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
		if !m.isAvailable(ctx, serverName) {
			return fmt.Sprintf("[MCP service unavailable] MCP server '%s' is not responding. Please try a different tool or check if the service is running.", serverName), nil
		}
		result, err := endpoint(ctx, argumentsInJSON, opts...)
		if err != nil {
			if _, ok := compose.IsInterruptRerunError(err); ok {
				return "", err
			}
			return fmt.Sprintf("[tool error] %v", err), nil
		}
		return result, nil
	}, nil
}

// WrapStreamableToolCall 流式调用的可用性检查。
func (m *mcpAvailMiddleware) WrapStreamableToolCall(
	ctx context.Context,
	endpoint adk.StreamableToolCallEndpoint,
	tCtx *adk.ToolContext,
) (adk.StreamableToolCallEndpoint, error) {
	serverName := m.getServerForTool(tCtx.Name)
	if serverName == "" {
		return endpoint, nil
	}

	return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (*schema.StreamReader[string], error) {
		if !m.isAvailable(ctx, serverName) {
			return singleStringChunk(fmt.Sprintf("[MCP service unavailable] MCP server '%s' is not responding. Please try a different tool or check if the service is running.", serverName)), nil
		}
		sr, err := endpoint(ctx, argumentsInJSON, opts...)
		if err != nil {
			if _, ok := compose.IsInterruptRerunError(err); ok {
				return nil, err
			}
			return singleStringChunk(fmt.Sprintf("[tool error] %v", err)), nil
		}
		return safeWrapReader(sr), nil
	}, nil
}

// isAvailable 检查 MCP 服务是否可用（带缓存）。
func (m *mcpAvailMiddleware) isAvailable(ctx context.Context, serverName string) bool {
	// 先查缓存
	if entry, ok := m.cache.Load(serverName); ok {
		e := entry.(mcpAvailEntry)
		if time.Now().Before(e.expires) {
			return e.avail
		}
	}

	// 防止同一 server 并发 ping
	m.pingMu.Lock()
	defer m.pingMu.Unlock()

	// 双重检查：可能其他 goroutine 已经 ping 过了
	if entry, ok := m.cache.Load(serverName); ok {
		e := entry.(mcpAvailEntry)
		if time.Now().Before(e.expires) {
			return e.avail
		}
	}

	// 执行 ping
	avail := m.pingServer(ctx, serverName)

	m.cache.Store(serverName, mcpAvailEntry{
		avail:   avail,
		expires: time.Now().Add(mcpAvailCheckInterval),
	})
	return avail
}

// pingServer 通过 MCP Ping 检查服务可用性。
func (m *mcpAvailMiddleware) pingServer(ctx context.Context, serverName string) bool {
	cli, ok := m.loader.GetClient(serverName)
	if !ok {
		return false
	}

	pingCtx, cancel := context.WithTimeout(ctx, mcpPingTimeout)
	defer cancel()

	return cli.Ping(pingCtx) == nil
}

// getServerForTool 获取工具所属的 MCP 服务名。
func (m *mcpAvailMiddleware) getServerForTool(toolName string) string {
	return m.toolToServer[toolName]
}
