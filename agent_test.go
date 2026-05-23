package agcore

import (
	"context"
	"os"
	"testing"

	"github.com/kinwyb/agcore/types"

	"github.com/cloudwego/eino/schema"
)

func TestNewAgent(t *testing.T) {
	baseURL := os.Getenv("OPENAI_BASE_URL")
	modelName := os.Getenv("OPENAI_MODEL_NAME")
	apiKey := os.Getenv("OPENAI_API_KEY")
	model, err := NewClaude(context.Background(), baseURL, modelName, apiKey)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &AgentConfig{
		Streaming:    true,
		MaxIteration: 5,
		Name:         "main",
		Description:  "",
		Instruction:  "",
		Type:         types.AgentTypeChat,
		LLM:          model,
		ToolReg:      nil,
		Middlewares:  nil,
		CheckStore:   nil,
		SubAgents:    nil,
		SkillDirs:    nil,
		MCPConfigs:   nil,
	}
	agent, err := NewAgent(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	content := "/Users/wangyingbin/Downloads/bodacli/skills 这个目录下有哪些文件"
	state := NewState("", "", schema.UserMessage(content), nil)
	err = agent.Prompt(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range state.NewMessage {
		t.Log(v.Content)
	}
}
