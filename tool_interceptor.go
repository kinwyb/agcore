package agcore

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// BeforeCallInput is the input passed to Before hooks before tool execution.
// Arguments can be modified by the hook to change what gets passed to the tool.
type BeforeCallInput struct {
	Context   context.Context // Request context, can access session/env values
	ToolName  string          // Tool name
	CallID    string          // Unique call identifier
	Arguments string          // JSON arguments string (modifiable)
}

// BeforeCallResult is the decision returned by a Before hook.
// If Skip is true, the tool execution is bypassed and SkipResult is returned.
// If Arguments is non-empty, it replaces the current arguments for subsequent hooks.
type BeforeCallResult struct {
	Skip       bool
	SkipResult string
	Arguments  string // Modified arguments (empty means no change)
}

// AfterCallInput is the input passed to After hooks after tool execution.
// Arguments reflects the actual arguments used for execution (after all Before hook modifications).
type AfterCallInput struct {
	ToolName  string
	CallID    string
	Arguments string // Actual arguments used for execution
	Result    string // Tool execution result
	Err       error  // Tool execution error (nil if successful)
}

// AfterCallResult is the decision returned by an After hook.
// If Result is non-empty, it replaces the current result.
type AfterCallResult struct {
	Result string // Modified result (empty means no change)
}

// ToolHook is the interface for intercepting tool calls.
// Implement this interface for hooks that need internal state or complex logic.
// For simple cases, use ToolHookFunc which wraps plain functions.
type ToolHook interface {
	Name() string
	Before(ctx context.Context, input *BeforeCallInput) (*BeforeCallResult, error)
	After(ctx context.Context, input *AfterCallInput) (*AfterCallResult, error)
}

// ToolHookFunc is a convenience struct that implements ToolHook using plain functions.
// Use this for simple hooks that don't need internal state.
type ToolHookFunc struct {
	HookName string
	BeforeFn func(context.Context, *BeforeCallInput) (*BeforeCallResult, error)
	AfterFn  func(context.Context, *AfterCallInput) (*AfterCallResult, error)
}

func (h *ToolHookFunc) Name() string { return h.HookName }

func (h *ToolHookFunc) Before(ctx context.Context, input *BeforeCallInput) (*BeforeCallResult, error) {
	if h.BeforeFn != nil {
		return h.BeforeFn(ctx, input)
	}
	return nil, nil
}

func (h *ToolHookFunc) After(ctx context.Context, input *AfterCallInput) (*AfterCallResult, error) {
	if h.AfterFn != nil {
		return h.AfterFn(ctx, input)
	}
	return nil, nil
}

// TimeoutRule is an optional interface hooks can implement to enforce tool execution timeouts.
// The interceptor checks for this interface and wraps endpoint calls with context.WithTimeout
// when a positive timeout is returned.
// Usage: ToolTimeoutHook([]string{"execute"}, 30*time.Second) returns a hook implementing this.
type TimeoutRule interface {
	// TimeoutFor returns the timeout duration for a given tool name.
	// Returns 0 to indicate no timeout for this tool.
	TimeoutFor(toolName string) time.Duration
}

// ToolTimeoutHook creates a hook that enforces execution timeout on specified tools.
// toolNames: list of tool names to apply timeout to (case-sensitive). Empty means all tools.
// timeout: maximum execution duration. Tools exceeding this are cancelled and return an error.
//
// Example:
//
//	hook := agcore.ToolTimeoutHook([]string{"execute"}, 30*time.Second)
//	agcore.AddToolInterceptorHook(hook)
func ToolTimeoutHook(toolNames []string, timeout time.Duration) ToolHook {
	h := &toolTimeout{timeout: timeout}
	if len(toolNames) > 0 {
		h.tools = make(map[string]bool, len(toolNames))
		for _, n := range toolNames {
			h.tools[n] = true
		}
	}
	return &ToolHookFunc{
		HookName: fmt.Sprintf("timeout(%v)", timeout),
		BeforeFn: h.before,
		AfterFn:  h.after,
	}
}

type toolTimeout struct {
	timeout time.Duration
	tools   map[string]bool // nil means all tools
}

func (h *toolTimeout) Name() string { return fmt.Sprintf("timeout(%v)", h.timeout) }

func (h *toolTimeout) TimeoutFor(toolName string) time.Duration {
	if h.tools != nil && !h.tools[toolName] {
		return 0
	}
	return h.timeout
}

func (h *toolTimeout) before(ctx context.Context, input *BeforeCallInput) (*BeforeCallResult, error) {
	// Just log the timeout enforcement; actual context wrapping is done by interceptor
	// via TimeoutRule interface
	return nil, nil
}

func (h *toolTimeout) after(ctx context.Context, input *AfterCallInput) (*AfterCallResult, error) {
	if input.Err != nil && ctx.Err() == context.DeadlineExceeded {
		return &AfterCallResult{
			Result: fmt.Sprintf("[tool execution timed out after %v]", h.timeout),
		}, nil
	}
	return nil, nil
}

// ToolInterceptor is a ChatModelAgentMiddleware that intercepts tool calls
// and runs registered hooks before and after execution.
// Hooks can audit, modify arguments/results, or block tool calls entirely.
type ToolInterceptor struct {
	*adk.BaseChatModelAgentMiddleware
	hooks []ToolHook
}

// NewToolInterceptor creates a new tool interceptor middleware.
// Hooks passed here are registered immediately. Use AddHook to add more later.
func NewToolInterceptor(hooks ...ToolHook) *ToolInterceptor {
	return &ToolInterceptor{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		hooks:                        hooks,
	}
}

// toolTimeoutFor checks all hooks for TimeoutRule and returns the shortest applicable timeout.
// Returns 0 if no hook imposes a timeout on this tool.
func (ti *ToolInterceptor) toolTimeoutFor(toolName string) time.Duration {
	var minTimeout time.Duration
	for _, hook := range ti.hooks {
		if tr, ok := hook.(TimeoutRule); ok {
			t := tr.TimeoutFor(toolName)
			if t > 0 && (minTimeout == 0 || t < minTimeout) {
				minTimeout = t
			}
		}
	}
	return minTimeout
}

// AddHook appends a hook to the interceptor. Hooks execute in registration order.
func (ti *ToolInterceptor) AddHook(hook ToolHook) {
	ti.hooks = append(ti.hooks, hook)
}

// WrapInvokableToolCall wraps synchronous tool execution with registered hooks.
func (ti *ToolInterceptor) WrapInvokableToolCall(
	ctx context.Context,
	endpoint adk.InvokableToolCallEndpoint,
	tCtx *adk.ToolContext,
) (adk.InvokableToolCallEndpoint, error) {
	if len(ti.hooks) == 0 {
		return endpoint, nil
	}

	return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
		args := argumentsInJSON

		// Run Before hooks in registration order
		for _, hook := range ti.hooks {
			in := &BeforeCallInput{
				Context:   ctx,
				ToolName:  tCtx.Name,
				CallID:    tCtx.CallID,
				Arguments: args,
			}
			res, err := hook.Before(ctx, in)
			if err != nil {
				return fmt.Sprintf("[hook error] hook '%s': %v", hook.Name(), err), nil
			}
			if res == nil {
				continue
			}
			if res.Skip {
				return res.SkipResult, nil
			}
			if res.Arguments != "" {
				args = res.Arguments
			}
		}

		// Execute tool: context.WithTimeout cancels underlying operation (e.g. kills child process),
		// goroutine select returns immediately on timeout even if tool ignores context.
		result, execErr := func() (string, error) {
			if timeout := ti.toolTimeoutFor(tCtx.Name); timeout > 0 {
				callCtx, cancel := context.WithTimeout(ctx, timeout)
				defer cancel()
				done := make(chan struct{})
				var r string
				var e error
				go func() {
					r, e = endpoint(callCtx, args, opts...)
					close(done)
				}()
				select {
				case <-done:
					return r, e
				case <-time.After(timeout):
					return "", fmt.Errorf("tool execution timed out after %v", timeout)
				}
			}
			return endpoint(ctx, args, opts...)
		}()

		// Run After hooks in reverse registration order
		for i := len(ti.hooks) - 1; i >= 0; i-- {
			hook := ti.hooks[i]
			in := &AfterCallInput{
				ToolName:  tCtx.Name,
				CallID:    tCtx.CallID,
				Arguments: args, // actual arguments used
				Result:    result,
				Err:       execErr,
			}
			res, err := hook.After(ctx, in)
			if err != nil {
				return fmt.Sprintf("[hook error] hook '%s' (after): %v", hook.Name(), err), nil
			}
			if res == nil {
				continue
			}
			if res.Result != "" {
				result = res.Result
			}
		}

		if execErr != nil {
			return "", execErr
		}
		return result, nil
	}, nil
}

// WrapStreamableToolCall wraps streaming tool execution with registered hooks.
func (ti *ToolInterceptor) WrapStreamableToolCall(
	ctx context.Context,
	endpoint adk.StreamableToolCallEndpoint,
	tCtx *adk.ToolContext,
) (adk.StreamableToolCallEndpoint, error) {
	if len(ti.hooks) == 0 {
		return endpoint, nil
	}

	return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (*schema.StreamReader[string], error) {
		args := argumentsInJSON

		// Run Before hooks
		for _, hook := range ti.hooks {
			in := &BeforeCallInput{
				Context:   ctx,
				ToolName:  tCtx.Name,
				CallID:    tCtx.CallID,
				Arguments: args,
			}
			res, err := hook.Before(ctx, in)
			if err != nil {
				return singleStringChunk(fmt.Sprintf("[hook error] hook '%s': %v", hook.Name(), err)), nil
			}
			if res == nil {
				continue
			}
			if res.Skip {
				return singleStringChunk(res.SkipResult), nil
			}
			if res.Arguments != "" {
				args = res.Arguments
			}
		}

		// Execute tool: wrap context with timeout — underlying operations (e.g. exec.CommandContext)
		// will be cancelled when timeout fires. Stream collection goroutine also respects this context.
		timeout := ti.toolTimeoutFor(tCtx.Name)
		var callCtx = ctx
		var cancel context.CancelFunc
		if timeout > 0 {
			callCtx, cancel = context.WithTimeout(ctx, timeout)
		}
		result, execErr := endpoint(callCtx, args, opts...)
		if cancel != nil {
			defer cancel()
		}
		if execErr != nil {
			return ti.wrapStreamResult(callCtx, tCtx, args, nil, execErr, timeout), nil
		}
		return ti.wrapStreamResult(callCtx, tCtx, args, result, nil, timeout), nil
	}, nil
}

// wrapStreamResult wraps a streaming tool result to run After hooks when complete.
func (ti *ToolInterceptor) wrapStreamResult(
	ctx context.Context,
	tCtx *adk.ToolContext,
	args string,
	input *schema.StreamReader[string],
	execErr error,
	timeout time.Duration,
) *schema.StreamReader[string] {
	outR, outW := schema.Pipe[string](64)

	go func() {
		defer outW.Close()

		// If execution failed before stream, send error to After hooks
		if execErr != nil {
			result := ""
			for i := len(ti.hooks) - 1; i >= 0; i-- {
				hook := ti.hooks[i]
				in := &AfterCallInput{
					ToolName:  tCtx.Name,
					CallID:    tCtx.CallID,
					Arguments: args,
					Result:    result,
					Err:       execErr,
				}
				res, err := hook.After(ctx, in)
				if err != nil {
					_ = outW.Send(fmt.Sprintf("[hook error] hook '%s' (after): %v", hook.Name(), err), nil)
					return
				}
				if res != nil && res.Result != "" {
					result = res.Result
				}
			}
			if result != "" {
				_ = outW.Send(result, nil)
			}
			return
		}

		defer input.Close()

		// Collect all chunks; ctx carries timeout so Recv unblocks when deadline fires.
		// Sub-goroutine + ctx.Done ensures we return even if stream hangs.
		type recvResult struct {
			chunks []string
			err    error
		}
		collected := make(chan recvResult, 1)
		go func() {
			var chunks []string
			for {
				chunk, err := input.Recv()
				if err == io.EOF {
					break
				}
				if err != nil {
					collected <- recvResult{chunks: chunks, err: err}
					return
				}
				chunks = append(chunks, chunk)
			}
			collected <- recvResult{chunks: chunks, err: nil}
		}()

		var chunks []string
		var recvErr error
		select {
		case res := <-collected:
			chunks, recvErr = res.chunks, res.err
		case <-ctx.Done():
			recvErr = fmt.Errorf("tool stream timed out after %v: %w", timeout, ctx.Err())
		}

		if recvErr != nil {
			// Run After hooks with error
			result := ""
			for i := len(ti.hooks) - 1; i >= 0; i-- {
				hook := ti.hooks[i]
				in := &AfterCallInput{
					ToolName:  tCtx.Name,
					CallID:    tCtx.CallID,
					Arguments: args,
					Result:    "",
					Err:       recvErr,
				}
				res, hookErr := hook.After(ctx, in)
				if hookErr != nil {
					_ = outW.Send(fmt.Sprintf("[hook error] hook '%s' (after): %v", hook.Name(), hookErr), nil)
					return
				}
				if res != nil && res.Result != "" {
					result = res.Result
				}
			}
			if result != "" {
				_ = outW.Send(result, nil)
			}
			return
		}

		fullResult := ""
		for _, c := range chunks {
			fullResult += c
		}

		// Run After hooks
		finalResult := fullResult
		for i := len(ti.hooks) - 1; i >= 0; i-- {
			hook := ti.hooks[i]
			in := &AfterCallInput{
				ToolName:  tCtx.Name,
				CallID:    tCtx.CallID,
				Arguments: args,
				Result:    fullResult,
				Err:       nil,
			}
			res, err := hook.After(ctx, in)
			if err != nil {
				_ = outW.Send(fmt.Sprintf("[hook error] hook '%s' (after): %v", hook.Name(), err), nil)
				return
			}
			if res != nil && res.Result != "" {
				finalResult = res.Result
			}
		}

		_ = outW.Send(finalResult, nil)
	}()

	return outR
}

// WrapEnhancedInvokableToolCall wraps enhanced synchronous tool execution with registered hooks.
func (ti *ToolInterceptor) WrapEnhancedInvokableToolCall(
	ctx context.Context,
	endpoint adk.EnhancedInvokableToolCallEndpoint,
	tCtx *adk.ToolContext,
) (adk.EnhancedInvokableToolCallEndpoint, error) {
	if len(ti.hooks) == 0 {
		return endpoint, nil
	}

	return func(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.ToolResult, error) {
		args := toolArgument.Text

		// Run Before hooks
		for _, hook := range ti.hooks {
			in := &BeforeCallInput{
				Context:   ctx,
				ToolName:  tCtx.Name,
				CallID:    tCtx.CallID,
				Arguments: args,
			}
			res, err := hook.Before(ctx, in)
			if err != nil {
				return nil, fmt.Errorf("hook '%s': %w", hook.Name(), err)
			}
			if res == nil {
				continue
			}
			if res.Skip {
				return textToolResult(res.SkipResult), nil
			}
			if res.Arguments != "" {
				args = res.Arguments
			}
		}

		// Execute tool: context.WithTimeout cancels underlying operation,
		// goroutine select returns immediately on timeout.
		result, execErr := func() (*schema.ToolResult, error) {
			if timeout := ti.toolTimeoutFor(tCtx.Name); timeout > 0 {
				callCtx, cancel := context.WithTimeout(ctx, timeout)
				defer cancel()
				done := make(chan struct{})
				var r *schema.ToolResult
				var e error
				go func() {
					r, e = endpoint(callCtx, &schema.ToolArgument{Text: args}, opts...)
					close(done)
				}()
				select {
				case <-done:
					return r, e
				case <-time.After(timeout):
					return nil, fmt.Errorf("tool execution timed out after %v", timeout)
				}
			}
			return endpoint(ctx, &schema.ToolArgument{Text: args}, opts...)
		}()

		// Run After hooks
		resultText := extractToolResultText(result)
		for i := len(ti.hooks) - 1; i >= 0; i-- {
			hook := ti.hooks[i]
			in := &AfterCallInput{
				ToolName:  tCtx.Name,
				CallID:    tCtx.CallID,
				Arguments: args,
				Result:    resultText,
				Err:       execErr,
			}
			res, err := hook.After(ctx, in)
			if err != nil {
				return nil, fmt.Errorf("hook '%s' (after): %w", hook.Name(), err)
			}
			if res != nil && res.Result != "" {
				resultText = res.Result
			}
		}

		if execErr != nil {
			return result, execErr
		}
		if resultText != "" {
			result = textToolResult(resultText)
		}
		return result, nil
	}, nil
}

// WrapEnhancedStreamableToolCall wraps enhanced streaming tool execution with registered hooks.
func (ti *ToolInterceptor) WrapEnhancedStreamableToolCall(
	ctx context.Context,
	endpoint adk.EnhancedStreamableToolCallEndpoint,
	tCtx *adk.ToolContext,
) (adk.EnhancedStreamableToolCallEndpoint, error) {
	if len(ti.hooks) == 0 {
		return endpoint, nil
	}

	return func(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.StreamReader[*schema.ToolResult], error) {
		args := toolArgument.Text

		// Run Before hooks
		for _, hook := range ti.hooks {
			in := &BeforeCallInput{
				Context:   ctx,
				ToolName:  tCtx.Name,
				CallID:    tCtx.CallID,
				Arguments: args,
			}
			res, err := hook.Before(ctx, in)
			if err != nil {
				return singleToolResultChunk(fmt.Sprintf("[hook error] hook '%s': %v", hook.Name(), err)), nil
			}
			if res == nil {
				continue
			}
			if res.Skip {
				return singleToolResultChunk(res.SkipResult), nil
			}
			if res.Arguments != "" {
				args = res.Arguments
			}
		}

		// Execute tool: wrap context with timeout for stream collection.
		timeout := ti.toolTimeoutFor(tCtx.Name)
		var callCtx = ctx
		var cancel context.CancelFunc
		if timeout > 0 {
			callCtx, cancel = context.WithTimeout(ctx, timeout)
		}
		result, execErr := endpoint(callCtx, &schema.ToolArgument{Text: args}, opts...)
		if cancel != nil {
			defer cancel()
		}
		if execErr != nil {
			return ti.wrapEnhancedStreamResult(callCtx, tCtx, args, nil, execErr, timeout), nil
		}
		return ti.wrapEnhancedStreamResult(callCtx, tCtx, args, result, nil, timeout), nil
	}, nil
}

// wrapEnhancedStreamResult wraps an enhanced streaming result to run After hooks when complete.
func (ti *ToolInterceptor) wrapEnhancedStreamResult(
	ctx context.Context,
	tCtx *adk.ToolContext,
	args string,
	input *schema.StreamReader[*schema.ToolResult],
	execErr error,
	timeout time.Duration,
) *schema.StreamReader[*schema.ToolResult] {
	outR, outW := schema.Pipe[*schema.ToolResult](64)

	go func() {
		defer outW.Close()

		if execErr != nil {
			resultText := ""
			for i := len(ti.hooks) - 1; i >= 0; i-- {
				hook := ti.hooks[i]
				in := &AfterCallInput{
					ToolName:  tCtx.Name,
					CallID:    tCtx.CallID,
					Arguments: args,
					Result:    resultText,
					Err:       execErr,
				}
				res, err := hook.After(ctx, in)
				if err != nil {
					_ = outW.Send(textToolResult(fmt.Sprintf("[hook error] hook '%s' (after): %v", hook.Name(), err)), nil)
					return
				}
				if res != nil && res.Result != "" {
					resultText = res.Result
				}
			}
			if resultText != "" {
				_ = outW.Send(textToolResult(resultText), nil)
			}
			return
		}

		defer input.Close()

		// Collect all results with timeout protection
		type recvResult struct {
			results []*schema.ToolResult
			err     error
		}
		collected := make(chan recvResult, 1)
		go func() {
			var results []*schema.ToolResult
			for {
				chunk, err := input.Recv()
				if err == io.EOF {
					break
				}
				if err != nil {
					collected <- recvResult{results: results, err: err}
					return
				}
				results = append(results, chunk)
			}
			collected <- recvResult{results: results, err: nil}
		}()

		var results []*schema.ToolResult
		var recvErr error
		select {
		case res := <-collected:
			results, recvErr = res.results, res.err
		case <-ctx.Done():
			recvErr = fmt.Errorf("tool stream timed out after %v: %w", timeout, ctx.Err())
		}

		if recvErr != nil {
			resultText := ""
			for i := len(ti.hooks) - 1; i >= 0; i-- {
				hook := ti.hooks[i]
				in := &AfterCallInput{
					ToolName:  tCtx.Name,
					CallID:    tCtx.CallID,
					Arguments: args,
					Result:    "",
					Err:       recvErr,
				}
				res, hookErr := hook.After(ctx, in)
				if hookErr != nil {
					_ = outW.Send(textToolResult(fmt.Sprintf("[hook error] hook '%s' (after): %v", hook.Name(), hookErr)), nil)
					return
				}
				if res != nil && res.Result != "" {
					resultText = res.Result
				}
			}
			if resultText != "" {
				_ = outW.Send(textToolResult(resultText), nil)
			}
			return
		}

		// Collect full text from results
		fullResult := ""
		for _, r := range results {
			fullResult += extractToolResultText(r)
		}

		// Run After hooks
		finalResult := fullResult
		for i := len(ti.hooks) - 1; i >= 0; i-- {
			hook := ti.hooks[i]
			in := &AfterCallInput{
				ToolName:  tCtx.Name,
				CallID:    tCtx.CallID,
				Arguments: args,
				Result:    fullResult,
				Err:       nil,
			}
			res, err := hook.After(ctx, in)
			if err != nil {
				_ = outW.Send(textToolResult(fmt.Sprintf("[hook error] hook '%s' (after): %v", hook.Name(), err)), nil)
				return
			}
			if res != nil && res.Result != "" {
				finalResult = res.Result
			}
		}

		_ = outW.Send(textToolResult(finalResult), nil)
	}()

	return outR
}

// extractToolResultText extracts text content from a ToolResult.
func extractToolResultText(tr *schema.ToolResult) string {
	if tr == nil {
		return ""
	}
	var sb strings.Builder
	for _, part := range tr.Parts {
		if part.Type == schema.ToolPartTypeText {
			sb.WriteString(part.Text)
		}
	}
	return sb.String()
}

// textToolResult creates a ToolResult with a single text part.
func textToolResult(text string) *schema.ToolResult {
	return &schema.ToolResult{
		Parts: []schema.ToolOutputPart{
			{Type: schema.ToolPartTypeText, Text: text},
		},
	}
}

// singleToolResultChunk creates a single-chunk stream reader for ToolResult.
func singleToolResultChunk(msg string) *schema.StreamReader[*schema.ToolResult] {
	r, w := schema.Pipe[*schema.ToolResult](1)
	_ = w.Send(textToolResult(msg), nil)
	w.Close()
	return r
}
