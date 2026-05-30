package bus

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
)

// 消息消费结构
type fanoutMessage[T any] struct {
	ctx       context.Context
	msg       chan *T
	subsriber map[string]subscriber[T]
	subMu     sync.RWMutex
	mu        sync.Mutex
	closed    bool
}

func newFanoutMessage[T any](ctx context.Context, bufferSize int) *fanoutMessage[T] {
	f := &fanoutMessage[T]{
		ctx:       ctx,
		msg:       make(chan *T, bufferSize),
		subsriber: make(map[string]subscriber[T]),
		subMu:     sync.RWMutex{},
	}
	go f.fanoutMessages()
	return f
}

func (f *fanoutMessage[T]) fanoutMessages() {
	defer func() {
		if err := recover(); err != nil {
			slog.Error("fanout messages recovered", err)
			go f.fanoutMessages()
		}
	}()
	for {
		select {
		case msg := <-f.msg:
			f.subMu.RLock()
			subCount := len(f.subsriber)
			f.subMu.RUnlock()

			if subCount == 0 {
				continue
			}

			// 转发到匹配的订阅者
			f.subMu.RLock()
			for _, sub := range f.subsriber {
				if sub.ignore(msg) {
					continue
				}
				sub.push(msg)
			}
			f.subMu.RUnlock()
		case <-f.ctx.Done():
			f.Close()
			slog.Info("fanout messages stopped")
			return
		}
	}
}

func (f *fanoutMessage[T]) Subscriber(sub subscriber[T]) string {
	f.subMu.Lock()
	defer f.subMu.Unlock()
	subID := uuid.New().String()
	f.subsriber[subID] = sub
	return subID
}

func (f *fanoutMessage[T]) Unsubscriber(subID string) {
	f.subMu.Lock()
	defer f.subMu.Unlock()
	delete(f.subsriber, subID)
}

func (f *fanoutMessage[T]) Push(ctx context.Context, msg *T) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return errors.New("fanout message bus closed")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case f.msg <- msg:
		return nil
	}
}

func (f *fanoutMessage[T]) Close() {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	defer f.mu.Unlock()
	f.closed = true
	f.subMu.Lock()
	defer f.subMu.Unlock()
	for _, sub := range f.subsriber {
		sub.Close()
	}
	close(f.msg)
}

// Error 总线错误
type Error struct {
	Message string
}

func (e *Error) Error() string {
	return e.Message
}

// Errors
var (
	ErrBusClosed               = &Error{Message: "message bus is closed"}
	ChannelSyncHandlerNotExist = &Error{Message: "channel has no sync handler"}
	ErrSubClosed               = &Error{Message: "subscriber is closed"}
)

// SyncMessageHandler 同步消息处理
type SyncMessageHandler func(ctx context.Context, tp SyncMessageType, msg *InboundMessage) (*OutboundMessage, error)

// MessageBus 消息总线
type MessageBus struct {
	cancel         context.CancelFunc
	inbound        *fanoutMessage[InboundMessage]
	outbound       *fanoutMessage[OutboundMessage]
	chatEvents     *fanoutMessage[Event]
	logEvents      *fanoutMessage[Log]
	syncMsgHandler map[string]SyncMessageHandler
	mu             sync.RWMutex
	closed         bool
}

// NewMessageBus 创建消息总线
func NewMessageBus(bufferSize int) *MessageBus {
	ctx, cancel := context.WithCancel(context.Background())
	b := &MessageBus{
		inbound:    newFanoutMessage[InboundMessage](ctx, bufferSize),
		outbound:   newFanoutMessage[OutboundMessage](ctx, bufferSize),
		chatEvents: newFanoutMessage[Event](ctx, bufferSize),
		logEvents:  newFanoutMessage[Log](ctx, bufferSize),
		closed:     false,
		cancel:     cancel,
	}
	return b
}

// PublishInbound 发布入站消息
func (b *MessageBus) PublishInbound(ctx context.Context, msg *InboundMessage) error {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if b.closed {
		return ErrBusClosed
	}

	// 设置ID和时间戳
	if msg.ID == "" {
		msg.ID = uuid.New().String()
	}
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now()
	}
	return b.inbound.Push(ctx, msg)
}

// SubscribeInbound 消费入站消息
func (b *MessageBus) SubscribeInbound(channel ...string) (*InboundSubscription, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if b.closed {
		return nil, ErrBusClosed
	}

	sub := newInboundSubscription(100, b.inbound)
	sub.channels = channel
	return sub, nil
}

// PublishOutbound 发布出站消息
func (b *MessageBus) PublishOutbound(ctx context.Context, msg *OutboundMessage) error {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if b.closed {
		return ErrBusClosed
	}

	// 设置ID和时间戳
	if msg.ID == "" {
		msg.ID = uuid.New().String()
	}
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now()
	}
	return b.outbound.Push(ctx, msg)
}

// SubscribeOutbound 消费出站消息
// 使用订阅机制，确保消息能够被正确接收
func (b *MessageBus) SubscribeOutbound(channel ...string) (*OutboundSubscription, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if b.closed {
		return nil, ErrBusClosed
	}

	sub := newOutboundSubscription(100, b.outbound)
	sub.channels = channel
	return sub, nil
}

// SubscribeSyncHandler 订阅同步消息处理机制
func (b *MessageBus) SubscribeSyncHandler(channel string, handler SyncMessageHandler) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return ErrBusClosed
	}
	if b.syncMsgHandler == nil {
		b.syncMsgHandler = make(map[string]SyncMessageHandler)
	}
	b.syncMsgHandler[channel] = handler
	return nil
}

// UnsubscribeSyncHandler 取消订阅同步消息处理机制
func (b *MessageBus) UnsubscribeSyncHandler(channel string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return ErrBusClosed
	}
	if b.syncMsgHandler == nil {
		return nil
	}
	delete(b.syncMsgHandler, channel)
	return nil
}

// SyncMessage 同步消息处理,tp消息类型
func (b *MessageBus) SyncMessage(ctx context.Context, tp SyncMessageType, msg *InboundMessage) (*OutboundMessage, error) {
	b.mu.RLock()
	handler := b.syncMsgHandler[msg.Channel]
	b.mu.RUnlock()
	if handler == nil {
		return nil, ChannelSyncHandlerNotExist
	}
	return handler(ctx, tp, msg)
}

// Close 关闭消息总线
func (b *MessageBus) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return nil
	}

	b.closed = true

	b.cancel()

	return nil
}

// IsClosed 检查是否已关闭
func (b *MessageBus) IsClosed() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.closed
}

// InboundCount 获取入站消息数量
func (b *MessageBus) InboundCount() int {
	return len(b.inbound.msg)
}

// OutboundCount 获取出站消息数量
func (b *MessageBus) OutboundCount() int {
	return len(b.outbound.msg)
}

// PublishEvent 发布事件
func (b *MessageBus) PublishEvent(ctx context.Context, event *Event) error {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if b.closed {
		return ErrBusClosed
	}

	// 设置默认值
	if event.ID == "" {
		event.ID = uuid.New().String()
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}
	return b.chatEvents.Push(ctx, event)

}

// SubscribeEvent 订阅聊天事件
func (b *MessageBus) SubscribeEvent(channel ...string) (*EventSubscription, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return nil, ErrBusClosed
	}

	sub := newEventSubscription(100, b.chatEvents)
	sub.channels = channel
	return sub, nil
}

// PublishLog 发布日志
func (b *MessageBus) PublishLog(ctx context.Context, event *Log) error {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if b.closed {
		return ErrBusClosed
	}

	// 设置默认值
	if event.ID == "" {
		event.ID = uuid.New().String()
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}
	return b.logEvents.Push(ctx, event)
}

// SubscribeLogEvent 订阅日志事件
func (b *MessageBus) SubscribeLogEvent() (*LogSubscription, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return nil, ErrBusClosed
	}

	sub := newLogSubscription(100, b.logEvents)
	return sub, nil
}
