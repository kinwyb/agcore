package tools

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func init() {
	schema.Register[*approvalInfo]()
}

type approvalInfo struct {
	ToolName        string
	ArgumentsInJSON string
	customPrompt    string // 自定义提示内容，由工具实现 ApprovalPrompter 接口时设置
}

func (ai *approvalInfo) ResumeParam(content string) any {
	// 根据消息内容解析用户意图
	content = strings.ToLower(strings.TrimSpace(content))
	approved := content == "y" || content == "yes" || content == "同意"

	var disapproveReason *string
	if !approved && content != "" {
		disapproveReason = new(content)
	}
	return &approvalResult{
		Approved:         approved,
		DisapproveReason: disapproveReason,
	}
}

// InterruptType 返回中断类型
func (ai *approvalInfo) InterruptType() string {
	return "yes_no"
}

func (ai *approvalInfo) InterruptReason() string {
	return ai.String()
}

func (ai *approvalInfo) String() string {
	if ai.customPrompt != "" {
		return ai.customPrompt
	}
	return fmt.Sprintf("工具 '%s' 正在用参数 '%s' 执行,等待你的确认审核. 请回复 Y/N ",
		ai.ToolName, ai.ArgumentsInJSON)
}

type approvalResult struct {
	Approved         bool
	DisapproveReason *string
}

// 工具执行审批中间件
type approveToolMiddleware struct {
	*adk.BaseChatModelAgentMiddleware
	toolReg *Registry
}

func (a *approveToolMiddleware) WrapInvokableToolCall(
	_ context.Context,
	endpoint adk.InvokableToolCallEndpoint,
	tCtx *adk.ToolContext,
) (adk.InvokableToolCallEndpoint, error) {

	// 只拦截需要审批的 Tool
	if !slices.Contains(a.toolReg.needApproveTool, tCtx.Name) {
		return endpoint, nil
	}

	return func(ctx context.Context, args string, opts ...tool.Option) (string, error) {
		wasInterrupted, _, storedArgs := tool.GetInterruptState[string](ctx)
		if !wasInterrupted {
			return "", tool.StatefulInterrupt(ctx, &approvalInfo{
				ToolName:        tCtx.Name,
				ArgumentsInJSON: args,
				customPrompt:    a.getApprovalPrompt(tCtx.Name, args),
			}, args)
		}

		isTarget, hasData, data := tool.GetResumeContext[*approvalResult](ctx)
		if isTarget && hasData {
			if data.Approved {
				return endpoint(ctx, storedArgs, opts...)
			}
			if data.DisapproveReason != nil {
				return fmt.Sprintf("tool '%s' disapproved: %s", tCtx.Name, *data.DisapproveReason), nil
			}
			return fmt.Sprintf("tool '%s' disapproved", tCtx.Name), nil
		}

		// 重新中断
		return "", tool.StatefulInterrupt(ctx, &approvalInfo{
			ToolName:        tCtx.Name,
			ArgumentsInJSON: storedArgs,
			customPrompt:    a.getApprovalPrompt(tCtx.Name, storedArgs),
		}, storedArgs)
	}, nil
}

func (a *approveToolMiddleware) WrapStreamableToolCall(
	_ context.Context,
	endpoint adk.StreamableToolCallEndpoint,
	tCtx *adk.ToolContext,
) (adk.StreamableToolCallEndpoint, error) {
	// 如果 agent 配置了 StreamingShell，则 execute 会走流式调用，需要实现该方法才能拦截到
	if !slices.Contains(a.toolReg.needApproveTool, tCtx.Name) {
		return endpoint, nil
	}

	return func(ctx context.Context, args string, opts ...tool.Option) (*schema.StreamReader[string], error) {
		wasInterrupted, _, storedArgs := tool.GetInterruptState[string](ctx)
		if !wasInterrupted {
			return nil, tool.StatefulInterrupt(ctx, &approvalInfo{
				ToolName:        tCtx.Name,
				ArgumentsInJSON: args,
				customPrompt:    a.getApprovalPrompt(tCtx.Name, args),
			}, args)
		}

		isTarget, hasData, data := tool.GetResumeContext[*approvalResult](ctx)
		if isTarget && hasData {
			if data.Approved {
				return endpoint(ctx, storedArgs, opts...)
			}
			if data.DisapproveReason != nil {
				return singleChunkReader(fmt.Sprintf("tool '%s' disapproved: %s", tCtx.Name, *data.DisapproveReason)), nil
			}
			return singleChunkReader(fmt.Sprintf("tool '%s' disapproved", tCtx.Name)), nil
		}

		isTarget, _, _ = tool.GetResumeContext[any](ctx)
		if !isTarget {
			return nil, tool.StatefulInterrupt(ctx, &approvalInfo{
				ToolName:        tCtx.Name,
				ArgumentsInJSON: storedArgs,
				customPrompt:    a.getApprovalPrompt(tCtx.Name, storedArgs),
			}, storedArgs)
		}

		return endpoint(ctx, storedArgs, opts...)
	}, nil
}

// 获取自定义的审核提示
func (a *approveToolMiddleware) getApprovalPrompt(toolName string, args string) string {
	return a.toolReg.getApprovalPrompt(toolName, args)
}

func singleChunkReader(msg string) *schema.StreamReader[string] {
	r, w := schema.Pipe[string](1)
	_ = w.Send(msg, nil)
	w.Close()
	return r
}

// NewToolApproveMiddleware 工具审核中间件
func NewToolApproveMiddleware(reg *Registry) adk.ChatModelAgentMiddleware {
	md := &approveToolMiddleware{
		toolReg: reg,
	}
	return md
}
