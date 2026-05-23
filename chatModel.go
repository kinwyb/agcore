package agcore

import (
	"context"
	"errors"

	"github.com/cloudwego/eino-ext/components/model/claude"
	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
)

// NewOpenAI 初始化openai模型
func NewOpenAI(ctx context.Context, baseURL string, modelName string, apiKey string) (model.ToolCallingChatModel, error) {
	if apiKey == "" {
		return nil, errors.New("openai: empty api key")
	} else if baseURL == "" {
		return nil, errors.New("openai: empty url")
	} else if modelName == "" {
		return nil, errors.New("openai: empty model name")
	}
	return openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey:          apiKey,
		Model:           modelName,
		BaseURL:         baseURL,
		ByAzure:         false,
		ReasoningEffort: openai.ReasoningEffortLevelHigh,
	})
}

// NewClaude 初始化claude模型
func NewClaude(ctx context.Context, baseURL string, modelName string, apiKey string) (model.ToolCallingChatModel, error) {
	if apiKey == "" {
		return nil, errors.New("claude: empty api key")
	} else if baseURL == "" {
		return nil, errors.New("claude: empty url")
	} else if modelName == "" {
		return nil, errors.New("claude: empty model name")
	}
	return claude.NewChatModel(ctx, &claude.Config{
		APIKey:    apiKey,
		BaseURL:   &baseURL,
		Model:     modelName,
		MaxTokens: 3000,
	})
}
