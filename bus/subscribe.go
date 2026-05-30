package bus

import (
	"context"
	"slices"
	"sync"
)

type subscriber[T any] interface {
	ignore(msg *T) bool
	push(msg *T)
	Close()
}

// subscription 消息订阅结构体
type subscription[T any] struct {
	subID    string
	Channel  chan *T
	channels []string // 过滤的 channels，空切片表示订阅所有
	fanout   *fanoutMessage[T]
	closed   bool
	mu       sync.Mutex
}

func (o *subscription[T]) push(msg *T) {
	o.Channel <- msg
}

// Consume 消费一条信息,bool标记是否还有后续消息
func (o *subscription[T]) Consume(ctx context.Context) (*T, bool) {
	if o.closed {
		return nil, false
	}
	select {
	case <-ctx.Done():
		return nil, false
	case msg := <-o.Channel:
		return msg, true
	}
}

func (o *subscription[T]) Close() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return
	}
	o.closed = true
	o.fanout.Unsubscriber(o.subID)
	close(o.Channel)
}

func newSubscription[T any](bufferSize int, fanout *fanoutMessage[T]) *subscription[T] {
	s := &subscription[T]{
		Channel: make(chan *T, bufferSize),
		fanout:  fanout,
	}
	return s
}

// OutboundSubscription 输出消息订阅
type OutboundSubscription struct {
	*subscription[OutboundMessage]
}

func (o *OutboundSubscription) ignore(msg *OutboundMessage) bool {
	return len(o.subscription.channels) > 0 && !slices.Contains(o.subscription.channels, msg.Channel)
}

func newOutboundSubscription(bufferSize int, fanout *fanoutMessage[OutboundMessage]) *OutboundSubscription {
	s := newSubscription[OutboundMessage](bufferSize, fanout)
	o := &OutboundSubscription{
		subscription: s,
	}
	o.subID = fanout.Subscriber(o)
	return o
}

// InboundSubscription 输入消息订阅
type InboundSubscription struct {
	*subscription[InboundMessage]
}

func (o *InboundSubscription) ignore(msg *InboundMessage) bool {
	return len(o.subscription.channels) > 0 && !slices.Contains(o.subscription.channels, msg.Channel)
}

func newInboundSubscription(bufferSize int, fanout *fanoutMessage[InboundMessage]) *InboundSubscription {
	s := newSubscription[InboundMessage](bufferSize, fanout)
	o := &InboundSubscription{
		subscription: s,
	}
	o.subID = fanout.Subscriber(o)
	return o
}

// EventSubscription 事件订阅
type EventSubscription struct {
	*subscription[Event]
}

func (e *EventSubscription) ignore(msg *Event) bool {
	return len(e.subscription.channels) > 0 && !slices.Contains(e.subscription.channels, msg.Channel)
}

func newEventSubscription(bufferSize int, fanout *fanoutMessage[Event]) *EventSubscription {
	s := newSubscription[Event](bufferSize, fanout)
	o := &EventSubscription{
		subscription: s,
	}
	o.subID = fanout.Subscriber(o)
	return o
}

// LogSubscription 日志订阅
type LogSubscription struct {
	*subscription[Log]
}

func (o *LogSubscription) ignore(msg *Log) bool {
	return false
}

func newLogSubscription(bufferSize int, fanout *fanoutMessage[Log]) *LogSubscription {
	s := newSubscription[Log](bufferSize, fanout)
	o := &LogSubscription{
		subscription: s,
	}
	o.subID = fanout.Subscriber(o)
	return o
}
