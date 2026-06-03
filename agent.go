package agcore

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/kinwyb/agcore/tools"
	"github.com/kinwyb/agcore/types"

	localbk "github.com/cloudwego/eino-ext/adk/backend/local"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/filesystem"
	"github.com/cloudwego/eino/adk/middlewares/skill"
	"github.com/cloudwego/eino/adk/prebuilt/deep"
	"github.com/cloudwego/eino/adk/prebuilt/planexecute"
	"github.com/cloudwego/eino/adk/prebuilt/supervisor"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/google/uuid"
)

type AgentConfig struct {
	Streaming    bool
	MaxIteration int // 模型最大循环次数
	Name         string
	Description  string                         // Agent 描述
	Instruction  string                         // 系统提示词
	Type         types.AgentType                // Agent 类型
	LLM          model.ToolCallingChatModel     // llm调用
	ToolReg      *tools.Registry                // 工具注册中心
	Middlewares  []adk.ChatModelAgentMiddleware // agent中间件
	CheckStore   adk.CheckPointStore            // 中断信息存储
	SubAgents    []*Agent                       // 子agent实例
	SkillDirs    []string                       // 支持多个SKILL目录
	MCPConfigs   []tools.MCPConfig              // MCP配置信息
	MCPLoader    *tools.MCPLoader               // MCP 工具加载器（用于可用性检查）
	// ModelRetryConfig configures retry behavior for the ChatModel.
	// Set via BuildModelRetryConfig or nil to disable retries.
	ModelRetryConfig *adk.ModelRetryConfig
	// ModelFailoverConfig configures failover behavior for the ChatModel.
	// When the primary model fails (after retries), switches to fallback models.
	ModelFailoverConfig *adk.ModelFailoverConfig
}

type Agent struct {
	cfg        *AgentConfig
	loop       *looper
	mu         sync.Mutex
	cancel     context.CancelFunc
	sessionMap map[string]*State
}

// NewAgent 创建一个 agent（根据类型自动选择）
func NewAgent(ctx context.Context, cfg *AgentConfig) (*Agent, error) {
	// 默认使用 DeepAgent
	if cfg.Type == "" {
		cfg.Type = types.AgentTypeDeep
	}
	if cfg.Name == "" {
		cfg.Name = "main_agent"
	}
	if cfg.Description == "" {
		cfg.Description = fmt.Sprintf("Agent %s for general tasks", cfg.Name)
	}
	if cfg.ToolReg == nil {
		cfg.ToolReg = tools.NewRegistry()
	}
	if cfg.Instruction == "" {
		cfg.Instruction = buildDefaultInstruction()
	}
	if cfg.CheckStore == nil {
		cfg.CheckStore = newInMemoryStore()
	}

	// 加载 MCP 工具
	mcpLoader, err := loadMCPTools(ctx, cfg.ToolReg, cfg.MCPConfigs)
	if err != nil {
		slog.Warn("Failed to load MCP tools", "error", err)
	}
	cfg.MCPLoader = mcpLoader

	switch cfg.Type {
	case types.AgentTypeChat:
		return newChatModelAgent(ctx, cfg)
	case types.AgentTypeDeep:
		return newDeepAgent(ctx, cfg)
	case types.AgentTypePlanExecute:
		return newPlanExecuteAgent(ctx, cfg)
	case types.AgentTypeSupervisor:
		return newSupervisorAgent(ctx, cfg)
	default:
		return nil, fmt.Errorf("unknown agent type: %s", cfg.Type)
	}
}

// defaultToolInterceptor is the global tool interceptor middleware.
// Use AddToolInterceptorHook to register hooks.
var defaultToolInterceptor = NewToolInterceptor()

// AddToolInterceptorHook registers a hook to the global tool interceptor.
// Hooks execute in registration order for Before, reverse order for After.
// This is safe to call from init() or any goroutine.
func AddToolInterceptorHook(hook ToolHook) {
	defaultToolInterceptor.AddHook(hook)
}

// agentMiddlewares agent的默认中间件
func agentMiddlewares(ctx context.Context, cfg *AgentConfig, fileMW bool) []adk.ChatModelAgentMiddleware {
	mds := append([]adk.ChatModelAgentMiddleware{
		defaultToolInterceptor, // 最外层，拦截所有工具调用
		tools.NewToolApproveMiddleware(cfg.ToolReg),
		NewToolValidateMiddleware(),
		NewMCPAvailMiddleware(cfg.MCPLoader),
	}, cfg.Middlewares...)
	if fileMW {
		toolAndShellMiddleware, _ := buildBuiltinAgentMiddlewares(ctx)
		if len(toolAndShellMiddleware) > 0 {
			mds = append(toolAndShellMiddleware, mds...)
		}
	}
	return mds
}

// 构建agent
func buildAgent(ctx context.Context, ag adk.Agent, cfg *AgentConfig) (*Agent, error) {
	loop, err := NewLooper(ctx, &looperConfig{
		agent:      ag,
		streaming:  cfg.Streaming,
		checkStore: cfg.CheckStore,
		llm:        cfg.LLM,
	})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)

	return &Agent{
		loop:       loop,
		cfg:        cfg,
		cancel:     cancel,
		sessionMap: make(map[string]*State),
	}, nil
}

// newChatModelAgent 创建基础的 ChatModelAgent（ReAct 模式）
func newChatModelAgent(ctx context.Context, cfg *AgentConfig) (*Agent, error) {
	agentConfig := &adk.ChatModelAgentConfig{
		Name:                cfg.Name,
		Description:         cfg.Description,
		Instruction:         cfg.Instruction,
		Model:               cfg.LLM,
		MaxIterations:       cfg.MaxIteration,
		Handlers:            agentMiddlewares(ctx, cfg, true),
		ModelRetryConfig:    cfg.ModelRetryConfig,
		ModelFailoverConfig: cfg.ModelFailoverConfig,
	}

	if cfg.ToolReg.ToolCount() > 0 {
		useTools, err := cfg.ToolReg.GetTools()
		if err != nil {
			return nil, fmt.Errorf("工具注册失败: %w", err)
		}
		agentConfig.ToolsConfig = adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: useTools,
			},
		}
	}

	ag, err := adk.NewChatModelAgent(ctx, agentConfig)
	if err != nil {
		return nil, err
	}

	return buildAgent(ctx, ag, cfg)
}

// newDeepAgent 创建 DeepAgent（规划+文件系统+子agent）
func newDeepAgent(ctx context.Context, cfg *AgentConfig) (*Agent, error) {
	description := cfg.Description

	backend, err := localbk.NewBackend(ctx, &localbk.Config{})
	if err != nil {
		return nil, fmt.Errorf("文件工具创建失败: %w", err)
	}

	// 构建子 agent 列表（adk.Agent 接口）
	subAgents := make([]adk.Agent, 0, len(cfg.SubAgents))
	for _, subAg := range cfg.SubAgents {
		if subAg.loop != nil && subAg.loop.agent != nil {
			subAgents = append(subAgents, subAg.loop.agent)
		}
	}
	// Instruction 占位符替换
	middlewares := make([]adk.ChatModelAgentMiddleware, 0, len(cfg.Middlewares)+1)
	middlewares = append(middlewares, newInstructionReplacerMiddleware())
	middlewares = append(middlewares, agentMiddlewares(ctx, cfg, false)...)
	if len(cfg.Middlewares) > 0 {
		middlewares = append(middlewares, cfg.Middlewares...)
	}
	agentConfig := &deep.Config{
		Name:                   cfg.Name,
		Description:            description,
		ChatModel:              cfg.LLM,
		Instruction:            cfg.Instruction,
		SubAgents:              subAgents,
		MaxIteration:           cfg.MaxIteration,
		Backend:                backend,
		Shell:                  backend,
		WithoutWriteTodos:      false,
		WithoutGeneralSubAgent: false,
		Handlers:               middlewares,
		ModelRetryConfig:       cfg.ModelRetryConfig,
		ModelFailoverConfig:    cfg.ModelFailoverConfig,
	}

	if cfg.ToolReg.ToolCount() > 0 {
		useTools, err := cfg.ToolReg.GetTools()
		if err != nil {
			return nil, fmt.Errorf("工具注册失败: %w", err)
		}
		agentConfig.ToolsConfig = adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: useTools,
			},
		}
	}

	if len(cfg.SkillDirs) > 0 {
		skillBackends, err := NewSkillBackends(ctx, backend, cfg.SkillDirs)
		if err != nil {
			return nil, fmt.Errorf("failed to create skill backends: %w", err)
		}
		if len(skillBackends) > 0 {
			multiSkillBackend := NewMultiSkillBackend(skillBackends...)
			skillMiddleware, err := skill.NewMiddleware(ctx, &skill.Config{
				Backend: multiSkillBackend,
			})
			if err != nil {
				return nil, fmt.Errorf("failed to create skill middleware: %w", err)
			}
			agentConfig.Handlers = append(agentConfig.Handlers, skillMiddleware)
		}
	}

	ag, err := deep.New(ctx, agentConfig)
	if err != nil {
		return nil, err
	}

	return buildAgent(ctx, ag, cfg)
}

// NewPlanExecuteAgent 创建 PlanExecute Agent（Plan-Execute-Replan 模式）
func newPlanExecuteAgent(ctx context.Context, cfg *AgentConfig) (*Agent, error) {
	// 创建 Planner
	planner, err := planexecute.NewPlanner(ctx, &planexecute.PlannerConfig{
		ToolCallingChatModel: cfg.LLM,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create planner: %w", err)
	}

	// 准备 Executor 的工具配置
	toolsConfig := adk.ToolsConfig{}
	if cfg.ToolReg.ToolCount() > 0 {
		useTools, err := cfg.ToolReg.GetTools()
		if err != nil {
			return nil, fmt.Errorf("工具注册失败: %w", err)
		}
		toolsConfig = adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: useTools,
			},
		}
	}

	// 创建 Executor
	executor, err := planexecute.NewExecutor(ctx, &planexecute.ExecutorConfig{
		Model:         cfg.LLM,
		ToolsConfig:   toolsConfig,
		MaxIterations: cfg.MaxIteration,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create executor: %w", err)
	}

	// 创建 Replanner
	replanner, err := planexecute.NewReplanner(ctx, &planexecute.ReplannerConfig{
		ChatModel: cfg.LLM,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create replanner: %w", err)
	}

	// 创建 PlanExecute agent
	ag, err := planexecute.New(ctx, &planexecute.Config{
		Planner:       planner,
		Executor:      executor,
		Replanner:     replanner,
		MaxIterations: cfg.MaxIteration,
	})
	if err != nil {
		return nil, err
	}

	return buildAgent(ctx, ag, cfg)
}

// NewSupervisorAgent 创建 Supervisor Agent（监督者模式）
func newSupervisorAgent(ctx context.Context, cfg *AgentConfig) (*Agent, error) {

	// 构建子 agent 列表（adk.Agent 接口）
	subAgents := make([]adk.Agent, 0, len(cfg.SubAgents))
	for _, subAg := range cfg.SubAgents {
		if subAg.loop != nil && subAg.loop.agent != nil {
			subAgents = append(subAgents, subAg.loop.agent)
		}
	}

	// 构建包含子 agent 信息的系统提示词
	systemPrompt := cfg.Instruction
	if len(subAgents) > 0 {
		systemPrompt += buildSubAgentPrompt(ctx, subAgents)
	}

	// 创建 supervisor agent（使用 ChatModelAgent 作为 supervisor）
	supervisorConfig := &adk.ChatModelAgentConfig{
		Name:                cfg.Name,
		Description:         cfg.Description,
		Instruction:         systemPrompt,
		Model:               cfg.LLM,
		MaxIterations:       cfg.MaxIteration,
		Handlers:            agentMiddlewares(ctx, cfg, true),
		ModelRetryConfig:    cfg.ModelRetryConfig,
		ModelFailoverConfig: cfg.ModelFailoverConfig,
	}

	supervisorAgent, err := adk.NewChatModelAgent(ctx, supervisorConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create supervisor agent: %w", err)
	}

	// 创建 Supervisor 结构
	supervisorConfig2 := &supervisor.Config{
		Supervisor: supervisorAgent,
		SubAgents:  subAgents,
	}

	ag, err := supervisor.New(ctx, supervisorConfig2)
	if err != nil {
		return nil, err
	}
	return buildAgent(ctx, ag, cfg)
}

// buildSubAgentPrompt 构建子 agent 描述提示词
func buildSubAgentPrompt(ctx context.Context, subAgents []adk.Agent) string {
	if len(subAgents) == 0 {
		return ""
	}

	var prompt strings.Builder
	prompt.WriteString("\n\n---\n\n## Sub-Agent Task Assignment\n\n")
	prompt.WriteString("You are a Supervisor Agent responsible for coordinating multiple sub-agents to complete tasks.")
	prompt.WriteString("You can delegate tasks to the following sub-agents:\n\n")

	for _, agent := range subAgents {
		name := agent.Name(ctx)
		desc := agent.Description(ctx)
		prompt.WriteString(fmt.Sprintf("- **%s**: %s\n", name, desc))
	}

	prompt.WriteString("\n### Task Assignment Guidelines\n\n")
	prompt.WriteString("Assign tasks to the appropriate sub-agent based on task type and the sub-agent's capability description.\n")
	prompt.WriteString("When delegating a task, provide a clear task description and expected output format.\n")
	prompt.WriteString("After a sub-agent completes a task, it will return the result. You need to integrate the results and decide the next action.\n")

	return prompt.String()
}

// buildBuiltinAgentMiddlewares 生成文件操作及shell执行中间件
func buildBuiltinAgentMiddlewares(ctx context.Context) ([]adk.ChatModelAgentMiddleware, error) {
	var ms []adk.ChatModelAgentMiddleware
	backend, err := localbk.NewBackend(ctx, &localbk.Config{})
	if err != nil {
		return nil, fmt.Errorf("文件工具创建失败: %w", err)
	}

	if backend != nil {
		fm, err := filesystem.New(ctx, &filesystem.MiddlewareConfig{
			Backend:        backend,
			Shell:          backend,
			StreamingShell: nil,
		})
		if err != nil {
			return nil, err
		}
		ms = append(ms, fm)
	}
	return ms, nil
}

// BuildModelRetryConfig 构建默认的重试配置。
// 仅在 429 限流和 5xx 服务器错误时重试，使用 eino 默认的指数退避策略。
func BuildModelRetryConfig(maxRetries int) *adk.ModelRetryConfig {
	return &adk.ModelRetryConfig{
		MaxRetries: maxRetries,
		ShouldRetry: func(ctx context.Context, retryCtx *adk.RetryContext) *adk.RetryDecision {
			if retryCtx.Err == nil {
				// 没有错误，接受结果
				return &adk.RetryDecision{Retry: false}
			}
			errStr := retryCtx.Err.Error()
			// 限流错误
			if strings.Contains(errStr, "429") ||
				strings.Contains(errStr, "Too Many Requests") ||
				strings.Contains(errStr, "qpm limit") ||
				strings.Contains(errStr, "rate limit") {
				return &adk.RetryDecision{Retry: true}
			}
			// 5xx 服务器错误
			if strings.Contains(errStr, "500") ||
				strings.Contains(errStr, "502") ||
				strings.Contains(errStr, "503") ||
				strings.Contains(errStr, "504") ||
				strings.Contains(errStr, "internal server error") ||
				strings.Contains(errStr, "bad gateway") ||
				strings.Contains(errStr, "service unavailable") {
				return &adk.RetryDecision{Retry: true}
			}
			// Context 取消不可重试
			if errors.Is(retryCtx.Err, context.Canceled) ||
				errors.Is(retryCtx.Err, context.DeadlineExceeded) {
				return &adk.RetryDecision{Retry: false}
			}
			// 未知错误不重试
			return &adk.RetryDecision{Retry: false}
		},
		// 使用 eino 默认的指数退避：100ms 基础，最大 10s，带随机抖动
	}
}

// buildDefaultInstruction 默认的提示词信息
func buildDefaultInstruction() string {
	return `
# Identity
You are a personal AI assistant running on the user's system.
You are NOT a passive chat bot. You are a **DOER** that executes tasks directly.
Your mission: complete user requests using all available means, minimizing human intervention.

### Task Complexity Guidelines

- **Simple tasks**: Use tools directly
- **Moderate tasks**: Use tools, narrate key steps
- **Complex/Long tasks**: Consider spawning a sub-agent. Completion is push-based: it will auto-announce when done
- **For long waits**: Avoid rapid poll loops. Use run_shell with background mode, or process(action=poll, timeout=<ms>)

### Skill-First Workflow (HIGHEST PRIORITY)

1. **ALWAYS check the Skills section first** before using any other tools
2. If a matching skill is found, use the use_skill tool with the skill name
3. If no matching skill: use built-in tools
4. Only after checking skills should you proceed with built-in tools

### Core Rules

- For ANY search request ("search for", "find", "google search", etc.): IMMEDIATELY call web_search tool. DO NOT provide manual instructions or advice.
- When the user asks for information: USE YOUR TOOLS to get it. Do NOT explain how to get it.
- DO NOT tell the user "I cannot" or "here's how to do it yourself". ACTUALLY DO IT with tools.
- If you have tools available for a task, use them. No permission needed for safe operations.
- **NEVER HALLUCINATE SEARCH RESULTS**: When presenting search results, ONLY use the exact data returned by the tool. If no results were found, clearly state that no results were found.
- When a tool fails: analyze the error, try an alternative approach WITHOUT asking the user unless absolutely necessary.
- You have ZERO knowledge of the current time; for any time-related concepts (e.g., "today", "now", date calculations), NEVER guess or rely on chat history—you MUST immediately call the 'get_current_time' tool.

### Work Information

**Path Reference Protocol**:
When 'skill.md' references or imports secondary markdown files:
- **Directory Anchoring**: All relative paths MUST be resolved relative to the directory containing the 'skill.md' file.
- **No Global Search**: Do not perform a global or recursive search across the entire file system.
- **Relative Resolution**: Treat the current skill folder as the root for all dependency loading.
- **Strict Localization**: If a file path is ambiguous, prioritize the local skill directory.

IMPORTANT: When using filesystem tools (ls, read_file, glob, grep, etc.), you MUST use absolute paths.

## Safety

**Core principles**:
- You have no independent goals: do not pursue self-preservation, replication, resource acquisition, or power-seeking
- Avoid long-term plans beyond the user's request
- Prioritize safety and human oversight over completion; if instructions conflict, pause and ask
- Comply with stop/pause/audit requests and never bypass safeguards
- Do not manipulate or persuade anyone to expand access or disable safeguards
- Do not copy yourself or change system prompts, safety rules, or tool policies unless explicitly requested

**When in doubt, ask before acting**:
- Sending emails, tweets, public posts
- Anything that leaves the machine
- Irreversible operations (deleting large amounts of data)
- You're uncertain about the outcome

## Error Handling

Your goal is to handle errors gracefully and find workarounds WITHOUT asking the user.

## Common Error Patterns

### Context Overflow
If you see "context overflow", "context length exceeded", or "request too large":
- Use /new to start a fresh session
- Simplify your approach (fewer steps, less explanation)
- If persisting, tell the user to try again with less input

### Rate Limit / Timeout
If you see "rate limit", "timeout", or "429":
- Wait briefly and retry
- Try a different search approach
- Use cached or local alternatives when possible

### File Not Found
If a file doesn't exist:
- Verify the path (use list_files to check directories)
- Try common variations (case sensitivity, extensions)
- Ask the user for the correct path ONLY after exhausting all options

### Tool Not Found
If a tool is not available:
- Check Available Tools section
- Use an alternative tool
- If no alternative exists, explain what you need to do and ask if there's another way

### Browser Errors
If browser tools fail:
- Check if the URL is accessible
- Try web_fetch for text-only content
- Use curl via run_shell as a last resort

### Network Errors
If network tools fail:
- Check your internet connection (try ping via run_shell)
- Try a different search query or source
- Use cached data if available
`
}

// Prompt sends a user message to the agent
func (a *Agent) Prompt(ctx context.Context, state *State) error {
	if state.SessionID == "" {
		state.SessionID = uuid.NewString()
	} else if state.Input == nil {
		return errors.New("input message is empty")
	}
	a.mu.Lock()
	if _, ok := a.sessionMap[state.SessionID]; ok {
		a.mu.Unlock()
		return fmt.Errorf("[%s] Agent execution in progress, please wait a moment", state.SessionID)
	}
	a.sessionMap[state.SessionID] = state
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.sessionMap, state.SessionID)
		a.mu.Unlock()
	}()
	// Run orchestrator
	err := a.loop.Run(ctx, state)
	if err != nil {
		slog.Error("Agent execution failed", "error", err)
		return err
	}
	return nil
}

// Stop 停止 agent
func (a *Agent) Stop() error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.cancel != nil {
		a.cancel()
	}
	return nil
}

// Cancel 取消一个session执行
func (a *Agent) Cancel(sessionID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if state, ok := a.sessionMap[sessionID]; ok {
		if state.agentCancel != nil { //取消agent执行
			handle, _ := state.agentCancel(
				adk.WithAgentCancelMode(adk.CancelAfterChatModel|adk.CancelAfterToolCalls),
				adk.WithAgentCancelTimeout(5*time.Second),
			)
			_ = handle.Wait()
		}
		if state.cancel != nil {
			state.cancel()
		}
		delete(a.sessionMap, sessionID)
	}
}

// Name agent名称
func (a *Agent) Name() string {
	return a.cfg.Name
}

// Instruction agent提示词
func (a *Agent) Instruction() string {
	return a.cfg.Instruction
}

// IsStreaming 是否流式输出
func (a *Agent) IsStreaming() bool {
	return a.cfg.Streaming
}

// instructionReplacerMiddleware 替换 Instruction 中的 SessionValue 占位符
type instructionReplacerMiddleware struct {
	*adk.BaseChatModelAgentMiddleware
}

// NewInstructionReplacerMiddleware 创建占位符替换中间件
func newInstructionReplacerMiddleware() adk.ChatModelAgentMiddleware {
	return &instructionReplacerMiddleware{}
}

func (m *instructionReplacerMiddleware) BeforeAgent(
	ctx context.Context,
	runCtx *adk.ChatModelAgentContext,
) (context.Context, *adk.ChatModelAgentContext, error) {
	if runCtx.Instruction == "" {
		return ctx, runCtx, nil
	}

	sessionValues := adk.GetSessionValues(ctx)
	if len(sessionValues) == 0 {
		return ctx, runCtx, nil
	}

	instruction := runCtx.Instruction
	for key, value := range sessionValues {
		placeholder := "{" + key + "}"
		if strings.Contains(instruction, placeholder) {
			instruction = strings.ReplaceAll(instruction, placeholder, fmt.Sprintf("%v", value))
		}
	}
	runCtx.Instruction = instruction

	return ctx, runCtx, nil
}
