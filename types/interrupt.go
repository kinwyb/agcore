package types

import (
	"github.com/cloudwego/eino/adk"
)

// IInterruptInfo 定义中断信息
type IInterruptInfo interface {
	// InterruptType 中断类型
	InterruptType() string

	// ResumeParam 中断恢复的参数,content恢复时收到的信息
	ResumeParam(content string) any

	// InterruptReason 中断原因
	InterruptReason() string
}

// InterruptCheckPoint 存储中断时的状态信息
type InterruptCheckPoint struct {
	InterruptID   string
	InterruptInfo IInterruptInfo //中断的信息
	Messages      []adk.Message  //中断之前的消息内容
}

// IApprovalPrompt 工具审批提示接口
// 工具可实现此接口来自定义审批提示内容
type IApprovalPrompt interface {
	// ApprovalPrompt 返回审批提示内容
	// argsJSON 是工具调用的 JSON 参数
	ApprovalPrompt(argsJSON string) string
}
