package agcore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// LLMJudgeConfig 配置默认的 LLM 判定插件。
type LLMJudgeConfig struct {
	// Model 评判用模型，可与主 agent 不同。必填。
	Model model.BaseChatModel
	// Prompt 自定义提示词，留空使用 defaultJudgePrompt。
	Prompt string
}

const defaultJudgePrompt = `你是一个任务进度评估器。下面是一个 AI Agent 与用户的对话；agent 因达到最大迭代次数而被中断。
请你判断这个任务是否还有继续执行的必要——如果 agent 已经接近完成、或者只剩收尾工作、或者还在朝着用户目标前进，则应该继续；
如果任务已经完成、或者陷入死循环、或者已经偏离用户目标无法挽回，则不应该继续。
如果应该继续，可以在 direction 字段给出下一步执行方向；如果没有特别方向，direction 为空字符串。

只输出严格的 JSON，禁止任何解释或 markdown 代码块包裹，格式：
{"continue": true|false, "reason": "<不超过 100 字的判断理由>", "direction": "<可选，下一步执行方向>"}`

// NewLLMMaxIterationsJudge 构造默认的 LLM 判定 MaxIterationsHandler。
//
// 调用方:
//
//	state.MaxIterationsHandler = agcore.NewLLMMaxIterationsJudge(&agcore.LLMJudgeConfig{Model: judgeModel})
//	state.MaxIterationsRetry = 3
//
// 解析失败 / 网络错 / 配置缺失时安全降级为 (false, nil)，并通过 slog 记录原因。
func NewLLMMaxIterationsJudge(cfg *LLMJudgeConfig) MaxIterationsHandler {
	if cfg == nil || cfg.Model == nil {
		return func(ctx context.Context, state *State) (bool, error) {
			slog.Error("LLMMaxIterationsJudge invoked with nil model")
			return false, nil
		}
	}
	prompt := cfg.Prompt
	if strings.TrimSpace(prompt) == "" {
		prompt = defaultJudgePrompt
	}

	return func(ctx context.Context, state *State) (bool, error) {
		convo := append(state.Input, state.NewMessage...)

		input := make([]*schema.Message, 0, len(convo)+2)
		input = append(input, schema.SystemMessage(prompt))
		input = append(input, convo...)
		input = append(input, schema.UserMessage("基于以上对话，请按要求 JSON 格式给出判断："))

		out, err := cfg.Model.Generate(ctx, input)
		if err != nil {
			slog.Error("LLMMaxIterationsJudge generate failed",
				"sessionID", state.SessionID, "error", err)
			return false, nil
		}
		if out == nil {
			slog.Error("LLMMaxIterationsJudge empty response", "sessionID", state.SessionID)
			return false, nil
		}

		cont, reason, direction, parseErr := parseJudgeOutput(out.Content)
		if parseErr != nil {
			slog.Error("LLMMaxIterationsJudge parse failed",
				"sessionID", state.SessionID, "error", parseErr, "raw", out.Content)
			return false, nil
		}
		if cont && strings.TrimSpace(direction) != "" {
			state.NewMessage = append(state.NewMessage, schema.UserMessage(direction))
		}
		slog.Info("LLMMaxIterationsJudge decision",
			"sessionID", state.SessionID, "continue", cont, "reason", reason, "direction", direction)
		return cont, nil
	}
}

// parseJudgeOutput 从模型输出里抽取 JSON 决策。容忍前后空白和 ```json``` 包裹。
func parseJudgeOutput(raw string) (bool, string, string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return false, "", "", errors.New("empty content")
	}
	// 去掉 markdown 围栏
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimPrefix(s, "```")
		if i := strings.LastIndex(s, "```"); i >= 0 {
			s = s[:i]
		}
		s = strings.TrimSpace(s)
	}
	// 截取首个 { 到末尾 }
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end <= start {
		return false, "", "", fmt.Errorf("no JSON object found")
	}
	s = s[start : end+1]

	var decision struct {
		Continue  bool   `json:"continue"`
		Reason    string `json:"reason"`
		Direction string `json:"direction"`
	}
	if err := json.Unmarshal([]byte(s), &decision); err != nil {
		return false, "", "", err
	}
	return decision.Continue, decision.Reason, decision.Direction, nil
}
