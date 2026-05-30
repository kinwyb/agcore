package agcore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type toolParameters interface {
	// Parameters 要求这是一个json schema结构
	Parameters() map[string]any
}

// toolValidateMiddleware validates tool call arguments against the tool's parameter
// schema before execution. Invalid arguments are rejected with a descriptive error
// returned to the model for self-correction.
//
// Schema compilation is lazy: each tool's schema is compiled on first invocation
// and cached for subsequent calls.
type toolValidateMiddleware struct {
	*adk.BaseChatModelAgentMiddleware

	mu      sync.RWMutex
	tools   map[string]tool.BaseTool // name -> tool reference (populated in BeforeAgent)
	schemas sync.Map                 // name -> *jsonschema.Schema (lazy compiled)
}

// NewToolValidateMiddleware creates a middleware that validates tool call arguments
// against each tool's JSON Schema before execution.
func NewToolValidateMiddleware() adk.ChatModelAgentMiddleware {
	return &toolValidateMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		tools:                        make(map[string]tool.BaseTool),
	}
}

// BeforeAgent populates the tool reference map. Schema compilation is deferred
// until the first Wrap* call for each tool.
func (m *toolValidateMiddleware) BeforeAgent(
	ctx context.Context,
	runCtx *adk.ChatModelAgentContext,
) (context.Context, *adk.ChatModelAgentContext, error) {
	// Reset for each agent run
	m.mu.Lock()
	m.tools = make(map[string]tool.BaseTool, len(runCtx.Tools))
	for _, t := range runCtx.Tools {
		if t == nil {
			continue
		}
		info, err := t.Info(ctx)
		if err != nil {
			continue // skip tools that fail to provide info
		}
		m.tools[info.Name] = t
	}
	m.mu.Unlock()

	return ctx, runCtx, nil
}

// WrapInvokableToolCall validates arguments before invoking the tool.
func (m *toolValidateMiddleware) WrapInvokableToolCall(
	ctx context.Context,
	endpoint adk.InvokableToolCallEndpoint,
	tCtx *adk.ToolContext,
) (adk.InvokableToolCallEndpoint, error) {
	validator := m.getValidator(ctx, tCtx.Name)
	if validator == nil {
		return endpoint, nil
	}

	return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
		if err := validator.Validate(argumentsInJSON); err != nil {
			return fmt.Sprintf("[parameter validation error] tool '%s': %s", tCtx.Name, err), nil
		}
		result, err := endpoint(ctx, argumentsInJSON, opts...)
		if err != nil {
			if _, ok := compose.IsInterruptRerunError(err); ok {
				return "", err
			}
			return fmt.Sprintf("[tool error] %v", err), nil
		}
		return result, nil
	}, nil
}

// WrapStreamableToolCall validates arguments before streaming tool invocation.
func (m *toolValidateMiddleware) WrapStreamableToolCall(
	ctx context.Context,
	endpoint adk.StreamableToolCallEndpoint,
	tCtx *adk.ToolContext,
) (adk.StreamableToolCallEndpoint, error) {
	validator := m.getValidator(ctx, tCtx.Name)
	if validator == nil {
		return endpoint, nil
	}

	return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (*schema.StreamReader[string], error) {
		if err := validator.Validate(argumentsInJSON); err != nil {
			return singleStringChunk(fmt.Sprintf("[parameter validation error] tool '%s': %s", tCtx.Name, err)), nil
		}
		sr, err := endpoint(ctx, argumentsInJSON, opts...)
		if err != nil {
			if _, ok := compose.IsInterruptRerunError(err); ok {
				return nil, err
			}
			return singleStringChunk(fmt.Sprintf("[tool error] %v", err)), nil
		}
		return safeWrapReader(sr), nil
	}, nil
}

// getValidator returns a compiled schema validator for the named tool.
// Returns nil if the tool has no parameter schema or is not found.
// Schema is compiled on first access and cached.
func (m *toolValidateMiddleware) getValidator(ctx context.Context, toolName string) *schemaValidator {
	// Check cache first
	if cached, ok := m.schemas.Load(toolName); ok {
		return cached.(*schemaValidator)
	}

	// Lookup tool reference
	m.mu.RLock()
	t, exists := m.tools[toolName]
	m.mu.RUnlock()
	if !exists {
		return nil
	}

	var schemaMap = map[string]any{}

	if tP, ok := t.(toolParameters); ok {
		schemaMap = tP.Parameters()
	} else {
		// Get tool info
		info, err := t.Info(ctx)
		if err != nil {
			return nil
		}
		if info.ParamsOneOf == nil {
			// No parameters defined, no validation needed
			m.schemas.Store(toolName, noValidator)
			return nil
		}

		// Convert to JSON Schema
		js, err := info.ParamsOneOf.ToJSONSchema()
		if err != nil || js == nil {
			m.schemas.Store(toolName, noValidator)
			return nil
		}

		schemaJSON, err := js.MarshalJSON()
		if err != nil {
			m.schemas.Store(toolName, noValidator)
			return nil
		}

		err = json.Unmarshal(schemaJSON, &schemaMap)
		if err != nil {
			m.schemas.Store(toolName, noValidator)
			return nil
		}
	}

	// Compile schema
	compiler := jsonschema.NewCompiler()
	compiler.AddResource(toolName+"schema.json", schemaMap)
	compiledSchema, err := compiler.Compile(toolName + "schema.json")
	if err != nil {
		m.schemas.Store(toolName, noValidator)
		return nil
	}

	validator := &schemaValidator{schema: compiledSchema}
	m.schemas.Store(toolName, validator)
	return validator
}

// schemaValidator wraps a compiled JSON schema for validating tool arguments.
type schemaValidator struct {
	schema *jsonschema.Schema
}

// Validate checks if the JSON arguments conform to the tool's parameter schema.
func (s *schemaValidator) Validate(argumentsInJSON string) error {
	if argumentsInJSON == "" || argumentsInJSON == "{}" {
		return nil
	}

	var v any
	if err := sonic.UnmarshalString(argumentsInJSON, &v); err != nil {
		return fmt.Errorf("invalid JSON arguments: %w", err)
	}
	return s.schema.Validate(v)
}

// noValidator is a sentinel value indicating no validation is needed for this tool.
var noValidator = &schemaValidator{}

// singleStringChunk creates a single-chunk stream reader for error messages.
func singleStringChunk(msg string) *schema.StreamReader[string] {
	r, w := schema.Pipe[string](1)
	_ = w.Send(msg, nil)
	w.Close()
	return r
}

func safeWrapReader(sr *schema.StreamReader[string]) *schema.StreamReader[string] {
	r, w := schema.Pipe[string](64)
	go func() {
		defer w.Close()
		for {
			chunk, err := sr.Recv()
			if errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				_ = w.Send(fmt.Sprintf("\n[tool error] %v", err), nil)
				return
			}
			_ = w.Send(chunk, nil)
		}
	}()
	return r
}
