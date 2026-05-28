package bus

import (
	"fmt"
	"time"

	"github.com/cloudwego/eino/schema"
)

type SyncMessageType string

const (
	SyncMessageTypeSendFile SyncMessageType = "send_file" // 发送文件
)

type StreamingMode string

// StreamingMode 常量定义（流式输出模式）
const (
	StreamingModeDelta      StreamingMode = "delta"      // 增量模式：Content 返回增量内容
	StreamingModeAccumulate StreamingMode = "accumulate" // 累积模式：Content 返回累积后的完整内容
)

// Media 媒体文件
type Media struct {
	Type     string         `json:"type"`               // image, video, audio, document
	URL      string         `json:"url"`                // 文件URL
	Base64   string         `json:"base64"`             // Base64编码内容
	MimeType string         `json:"mimetype"`           // MIME类型
	Metadata map[string]any `json:"metadata,omitempty"` // 额外元数据（如加密参数等）
}

// InboundMessage 入站消息
type InboundMessage struct {
	ID            string         `json:"id"`
	Channel       string         `json:"channel"`        // 来源渠道
	AccountID     string         `json:"account_id"`     // 账号ID（用于多账号场景）
	SenderID      string         `json:"sender_id"`      // 发送者ID
	ChatID        string         `json:"chat_id"`        // 聊天ID
	Content       string         `json:"content"`        // 消息内容
	Media         []Media        `json:"media"`          // 媒体文件
	StreamingMode StreamingMode  `json:"streaming_mode"` // 流式输出模式："delta" 增量, "accumulate" 累积
	Metadata      map[string]any `json:"metadata"`       // 元数据
	Timestamp     time.Time      `json:"timestamp"`
}

// SessionKey 返回会话键
func (m *InboundMessage) SessionKey() string {
	sessionKey := fmt.Sprintf("%s_%s_%s", m.Channel, m.AccountID, m.ChatID)
	if m.ChatID == "default" || m.ChatID == "" {
		// 使用日期格式，同一天共用一个 session
		sessionKey = fmt.Sprintf("%s_%s_%s", m.Channel, m.AccountID, m.Timestamp.Format("2006-01-02"))
	}
	return sessionKey
}

// AgentMessage 输入消息转换成agent消息
func (m *InboundMessage) AgentMessage() *schema.Message {
	return buildUserMessage(m.Content, m.Media)
}

// OutboundMessage 出站消息
type OutboundMessage struct {
	ID               string                 `json:"id"`
	Channel          string                 `json:"channel"`           // 使用 Channel* 常量
	ChatID           string                 `json:"chat_id"`           // 聊天ID
	Content          string                 `json:"content"`           // 消息内容（根据 StreamingMode 决定是增量还是累积）
	ReasoningContent string                 `json:"reasoning_content"` // 思考/推理内容
	IsStreaming      bool                   `json:"is_streaming"`      // 是否流式发送
	IsThinking       bool                   `json:"is_thinking"`       // 是否为思考内容（流式时使用）
	IsFinal          bool                   `json:"is_final"`          // 是否最终消息（流式时使用）
	ChunkIndex       int                    `json:"chunk_index"`       // chunk序号（流式时使用）
	Media            []Media                `json:"media"`             // 媒体文件
	ReplyTo          string                 `json:"reply_to"`          // 回复的消息ID
	Error            string                 `json:"error,omitempty"`   // 错误信息
	Metadata         map[string]interface{} `json:"metadata"`          // 元数据
	Timestamp        time.Time              `json:"timestamp"`
}

// EventType 事件类型
type EventType string

// EventType 事件类型
const (
	EventTypeStart     EventType = "start"     // 开始处理（UI 显示 loading）
	EventTypeTool      EventType = "tool"      // 工具调用通知
	EventTypeComplete  EventType = "complete"  // 处理完成（UI 隐藏 loading）
	EventTypeError     EventType = "error"     // 错误通知
	EventTypeInterrupt EventType = "interrupt" // 中断通知
)

// Event 消息事件（用于状态通知）
type Event struct {
	Seq       int            `json:"seq"` //事件顺序ID
	Type      EventType      `json:"type"`
	ID        string         `json:"id"`
	Channel   string         `json:"channel"`
	ChatID    string         `json:"chat_id"`
	ReplyTo   string         `json:"reply_to"`   // 关联的入站消息ID，与 OutboundMessage.ReplyTo 一致
	AgentName string         `json:"agent_name"` // Agent 名称
	Error     string         `json:"error,omitempty"`
	ToolInfo  *ToolEventInfo `json:"tool_info,omitempty"` // 工具信息（tool 状态）
	Timestamp time.Time      `json:"timestamp"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// ToolEventInfo 工具事件信息
type ToolEventInfo struct {
	Name      string `json:"name"`                // 工具名称
	ID        string `json:"id"`                  // 工具调用ID
	Arguments string `json:"arguments,omitempty"` // 工具参数（开始时）
	Result    string `json:"result,omitempty"`    // 工具结果（结束时）
	IsStart   bool   `json:"is_start"`            // true=开始, false=结束
}

// Log 日志事件（用于系统日志输出）
type Log struct {
	ID        string    `json:"id"`
	Level     string    `json:"level"` // "debug", "info", "warn", "error"
	Message   string    `json:"message"`
	Timestamp time.Time `json:"timestamp"`
	Source    string    `json:"source"` // 来源组件
}

// LogEvent levels
const (
	LogLevelDebug = "debug"
	LogLevelInfo  = "info"
	LogLevelWarn  = "warn"
	LogLevelError = "error"
)
