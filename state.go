package agcore

import (
	"context"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/compose"
	"github.com/google/uuid"
)

// MessageExtraReqIDKey 消息扩展信息中的请求消息ID
const MessageExtraReqIDKey = "req_id"

// MessageExtraTimestampKey 消息扩展信息中的消息时间
const MessageExtraTimestampKey = "timestamp"

type EventType string

const (
	EventMessageStart  EventType = "message_start"
	EventMessageUpdate EventType = "message_update"
	EventMessageEnd    EventType = "message_end"
	EventToolStart     EventType = "tool_start"
	EventToolEnd       EventType = "tool_end"
	EventInterrupt     EventType = "interrupt"
)

type Event struct {
	Type      EventType              `json:"type"`
	Message   adk.Message            `json:"message,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	Timestamp int64                  `json:"timestamp"`
}

// NewEvent creates a new event with current timestamp
func NewEvent(eventType EventType) *Event {
	return &Event{
		Type:      eventType,
		Timestamp: time.Now().UnixMilli(),
	}
}

// WithMessage adds message to the event
func (e *Event) WithMessage(msg adk.Message) *Event {
	e.Message = msg
	return e
}

// WithMetadata adds metadata to the event
func (e *Event) WithMetadata(metadata map[string]interface{}) *Event {
	e.Metadata = metadata
	return e
}

// EventCallback 事件回调函数
type EventCallback func(event *Event)

func defaultEventCallback(event *Event) {
	// ignore event
}

// State 请求的状态信息
type State struct {
	ctx            context.Context     //上下文
	cancel         context.CancelFunc  //取消函数
	agentCancel    adk.AgentCancelFunc //agent取消
	SessionID      string              `json:"session_id"`      // 会话记录ID
	ReqID          string              `json:"req_id"`          // 输入消息ID
	Input          []adk.Message       `json:"input"`           // 输入消息
	HistoryMessage []adk.Message       `json:"history_message"` // 历史消息
	NewMessage     []adk.Message       `json:"new_message"`     // 新消息
	Session        map[string]any      `json:"session"`         // session值
	EventHandler   EventCallback       `json:"-"`               // 消息事件处理
}

// FullMessages 获取state完整消息，包含：历史消息，输入消息，新消息
func (s *State) FullMessages() []adk.Message {
	message := append(s.HistoryMessage, s.Input...)
	message = append(message, s.NewMessage...)
	return message
}

// AnswerMessages 获取state最新问题的数据，包含：输入消息，新消息
func (s *State) AnswerMessages() []adk.Message {
	message := append(s.Input, s.NewMessage...)
	return message
}

// LastAnswer 获取state最后的回答,大模型返回的最后消息
func (s *State) LastAnswer() adk.Message {
	if len(s.NewMessage) == 0 {
		return nil
	}
	return s.NewMessage[len(s.NewMessage)-1]
}

// LastInput 获取state中最后一条输入的消息
func (s *State) LastInput() adk.Message {
	if len(s.Input) == 0 {
		return nil
	}
	return s.Input[len(s.Input)-1]
}

// NewState 创建一个消息状态
func NewState(sessionID string, reqID string, input adk.Message, history []adk.Message) *State {
	if sessionID == "" {
		sessionID = uuid.NewString()
	}
	if reqID == "" {
		reqID = uuid.New().String()
	}
	return &State{
		SessionID:      sessionID,
		ReqID:          reqID,
		Input:          []adk.Message{input},
		HistoryMessage: history,
		NewMessage:     nil,
		Session:        make(map[string]any),
		EventHandler:   nil,
	}
}

// NewStateMuiltInput 创建一个消息状态
func NewStateMuiltInput(sessionID string, reqID string, input []adk.Message, history []adk.Message) *State {
	if sessionID == "" {
		sessionID = uuid.NewString()
	}
	if reqID == "" {
		reqID = uuid.New().String()
	}
	return &State{
		SessionID:      sessionID,
		ReqID:          reqID,
		Input:          input,
		HistoryMessage: history,
		NewMessage:     nil,
		Session:        make(map[string]any),
		EventHandler:   nil,
	}
}

// NewInMemoryStore 中断的内存存储
func newInMemoryStore() compose.CheckPointStore {
	return &inMemoryStore{
		mem: map[string][]byte{},
	}
}

type inMemoryStore struct {
	mem map[string][]byte
}

func (i *inMemoryStore) Set(ctx context.Context, key string, value []byte) error {
	i.mem[key] = value
	return nil
}

func (i *inMemoryStore) Get(ctx context.Context, key string) ([]byte, bool, error) {
	v, ok := i.mem[key]
	return v, ok, nil
}
