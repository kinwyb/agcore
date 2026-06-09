package agcore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/kinwyb/agcore/types"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type looperConfig struct {
	agent      adk.Agent
	streaming  bool
	checkStore adk.CheckPointStore
}

type looper struct {
	agent        adk.Agent
	runner       *adk.Runner
	checkPointMu sync.Mutex
	checkPoints  map[string]*types.InterruptCheckPoint // 按sessionID存储中断检查点
}

func NewLooper(ctx context.Context, cfg *looperConfig) (*looper, error) {
	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           cfg.agent,
		EnableStreaming: cfg.streaming,
		CheckPointStore: cfg.checkStore,
	})
	o := &looper{
		agent:        cfg.agent,
		runner:       runner,
		checkPointMu: sync.Mutex{},
		checkPoints:  make(map[string]*types.InterruptCheckPoint),
	}
	return o, nil
}

// Run starts the agent loop with initial prompts
func (o *looper) Run(ctx context.Context, state *State) error {
	slog.Debug("=== Looper Run Start ===")

	state.ctx, state.cancel = context.WithCancel(ctx)

	if state.EventHandler == nil {
		state.EventHandler = defaultEventCallback
	}

	// Main loop
	err := o.runLoop(state)

	if err == nil && len(state.NewMessage) < 1 {
		return fmt.Errorf("agent loop failed: result msg empty")
	}
	if err != nil {
		if _, ok := compose.IsInterruptRerunError(err); ok {
			return err
		}
		if errors.Is(err, adk.ErrExceedMaxIterations) {
			return errors.New("agent运行已打最大允许的轮次本次任务中止,如需继续任务请输入继续")
		}
		return fmt.Errorf("agent loop failed: %w", err)
	}

	state.cancel()

	return nil
}

func (o *looper) runLoop(state *State) error {
	if _, ok := o.checkPoints[state.SessionID]; ok { //存在断点信息，执行断点恢复操作
		return o.runResume(state)
	}

	var opts []adk.AgentRunOption
	sessionValue := state.Session
	if sessionValue == nil {
		sessionValue = make(map[string]any)
	}
	sessionValue[types.SessionValueWithAgentName] = o.agent.Name(state.ctx)

	opts = append(opts, adk.WithSessionValues(sessionValue))

	messages := append(state.HistoryMessage, state.Input...)

	cancelOpt, cancelFunc := adk.WithCancel()

	state.agentCancel = cancelFunc

	opts = append(opts, cancelOpt)
	events := o.runner.Run(state.ctx, messages, opts...)
	return o.processEvents(state, events)
}

// Resume 恢复被中断的执行
func (o *looper) runResume(state *State) error {
	o.checkPointMu.Lock()
	cp, exists := o.checkPoints[state.SessionID]
	if !exists {
		o.checkPointMu.Unlock()
		return o.runLoop(state)
	}
	// 恢复后删除该断点
	delete(o.checkPoints, state.SessionID)
	o.checkPointMu.Unlock()

	input := state.LastInput()

	if input == nil {
		return errors.New("agent loop failed: input nil")
	}

	// 校验参数类型
	var param any
	if cp.InterruptInfo != nil {
		param = cp.InterruptInfo.ResumeParam(input.Content)
	}
	state.NewMessage = cp.Messages

	slog.Debug("Resuming from interrupt",
		"interruptID", cp.InterruptID,
		"resumeInput", input.Content,
		"resumeParam", param)

	// 使用ResumeWithParams恢复执行
	events, err := o.runner.ResumeWithParams(state.ctx, state.SessionID, &adk.ResumeParams{
		Targets: map[string]any{
			cp.InterruptID: param,
		},
	})
	if err != nil {
		return fmt.Errorf("resume failed: %w", err)
	}

	return o.processEvents(state, events)
}

// processEvents 处理runner收到的消息逻辑
func (o *looper) processEvents(state *State, events *adk.AsyncIterator[*adk.AgentEvent]) error {
	msgID := state.ReqID
	for {
		event, ok := events.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			if cancelErr, ok2 := errors.AsType[*adk.CancelError](event.Err); ok2 {
				slog.Warn("Agent 被取消", "mode", cancelErr.Info.Mode, "escalated", cancelErr.Info.Escalated)
			}
			return event.Err
		}

		// 检测中断事件
		if event.Action != nil && event.Action.Interrupted != nil {
			interruptInfo := o.handleInterrupt(state, event)
			if interruptInfo == "" {
				interruptInfo = "Interrupted"
			}
			// 中断后返回，等待用户恢复
			return compose.Interrupt(state.ctx, interruptInfo)
		}

		if event.Output != nil && event.Output.MessageOutput != nil {

			state.EventHandler(NewEvent(EventMessageStart))

			mv := event.Output.MessageOutput

			msg, err := o.messageParse(state.EventHandler, mv)
			if err != nil {
				return err
			}

			if mv.Role == schema.Tool {
				state.EventHandler(NewEvent(EventToolEnd).WithMessage(msg))
			} else if mv.Role == schema.Assistant && len(msg.ToolCalls) > 0 {
				state.EventHandler(NewEvent(EventToolStart).WithMessage(msg))
			} else {
				state.EventHandler(NewEvent(EventMessageEnd).WithMessage(msg))
			}
			if msg.Extra == nil {
				msg.Extra = make(map[string]interface{})
			}
			if tv, hasT := msg.Extra[MessageExtraTimestampKey]; !hasT || tv == "" {
				msg.Extra[MessageExtraTimestampKey] = time.Now().Format(time.RFC3339)
			}
			if iv, hasI := msg.Extra[MessageExtraReqIDKey]; !hasI || iv == "" {
				msg.Extra[MessageExtraReqIDKey] = msgID
			}
			state.NewMessage = append(state.NewMessage, msg)
		}
	}
	return nil
}

func (o *looper) messageParse(callback EventCallback, mv *adk.MessageVariant) (adk.Message, error) {
	if mv.IsStreaming {
		mv.MessageStream.SetAutomaticClose()
		var msgs []adk.Message
		for {
			msg, err := mv.MessageStream.Recv()
			if err != nil {
				if err == io.EOF {
					break
				}
				return nil, err
			}
			callback(NewEvent(EventMessageUpdate).WithMessage(msg))
			msgs = append(msgs, msg)
		}
		return schema.ConcatMessages(msgs)
	}
	return mv.Message, nil
}

// handleInterrupt 处理中断事件
func (o *looper) handleInterrupt(state *State, event *adk.AgentEvent) string {
	if event.Action == nil || event.Action.Interrupted == nil {
		return ""
	}

	if state.SessionID == "" {
		slog.Warn("Interrupt occurred but no checkPointID provided")
		return ""
	}

	o.checkPointMu.Lock()
	defer o.checkPointMu.Unlock()
	if _, ok := o.checkPoints[state.SessionID]; ok {
		slog.Warn("Interruption occurred, but checkPointID duplicate")
		return ""
	}
	sb := strings.Builder{}
	// 获取中断上下文
	if len(event.Action.Interrupted.InterruptContexts) > 0 {
		for _, point := range event.Action.Interrupted.InterruptContexts {
			// 获取端点参数类型
			var interruptInfo types.IInterruptInfo
			interruptType := "unknown"
			interruptReason := "Interrupted"
			if point.Info != nil {
				if provider, ok := point.Info.(types.IInterruptInfo); ok {
					interruptInfo = provider
					interruptType = provider.InterruptType()
					interruptReason = provider.InterruptReason()
				}
			}

			o.checkPoints[state.SessionID] = &types.InterruptCheckPoint{
				InterruptID:   point.ID,
				InterruptInfo: interruptInfo,
				Messages:      state.NewMessage,
			}

			// 构建 metadata
			metadata := map[string]any{
				"interrupt_type": interruptType,
			}

			// 发送中断事件
			state.EventHandler(NewEvent(EventInterrupt).WithMessage(&schema.Message{
				Role:    schema.Assistant,
				Content: interruptReason,
			}).WithMetadata(metadata))
			sb.WriteString(interruptReason + "\n")
		}
	}
	return sb.String()
}
