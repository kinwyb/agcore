package types

// AgentType agent 类型
type AgentType string

const (
	AgentTypeChat        AgentType = "chat"        // 基础 ReAct 模式
	AgentTypeDeep        AgentType = "deep"        // 预构建 agent（规划+文件系统+子agent）
	AgentTypePlanExecute AgentType = "planexecute" // Plan-Execute-Replan 模式
	AgentTypeSupervisor  AgentType = "supervisor"  // 监督者模式
)
