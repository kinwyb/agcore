package agcore

import (
	"context"
	"errors"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestParseJudgeOutput(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		wantCont bool
		wantErr  bool
		wantPart string
		wantDir  string
	}{
		{name: "valid json continue", raw: `{"continue": true, "reason": "still progressing"}`, wantCont: true, wantPart: "still"},
		{name: "valid json stop", raw: `{"continue": false, "reason": "done"}`, wantCont: false, wantPart: "done"},
		{name: "with direction", raw: `{"continue": true, "reason": "still", "direction": "先检查测试失败原因"}`, wantCont: true, wantPart: "still", wantDir: "先检查测试失败原因"},
		{name: "wrapped fence", raw: "```json\n{\"continue\": true, \"reason\": \"ok\"}\n```", wantCont: true, wantPart: "ok"},
		{name: "with prose", raw: "Here is my decision:\n{\"continue\": false, \"reason\": \"finished\"}\nThanks", wantCont: false, wantPart: "finished"},
		{name: "empty", raw: "", wantErr: true},
		{name: "no json", raw: "not a json", wantErr: true},
		{name: "broken json", raw: `{"continue": notbool}`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cont, reason, direction, err := parseJudgeOutput(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cont != tc.wantCont {
				t.Errorf("continue=%v want %v", cont, tc.wantCont)
			}
			if tc.wantPart != "" && reason == "" {
				t.Errorf("reason empty, expected to contain %q", tc.wantPart)
			}
			if direction != tc.wantDir {
				t.Errorf("direction=%q want %q", direction, tc.wantDir)
			}
		})
	}
}

// stubModel implements model.BaseChatModel for judge tests.
type stubModel struct {
	resp *schema.Message
	err  error
}

func (s *stubModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return s.resp, s.err
}

func (s *stubModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("not implemented")
}

func TestLLMMaxIterationsJudge(t *testing.T) {
	mkState := func() *State {
		st := NewState("sess", "req", schema.UserMessage("帮我列出文件"), nil)
		st.NewMessage = []*schema.Message{schema.AssistantMessage("正在执行...", nil)}
		return st
	}

	t.Run("nil cfg returns false", func(t *testing.T) {
		h := NewLLMMaxIterationsJudge(nil)
		cont, err := h(context.Background(), mkState())
		if err != nil || cont {
			t.Fatalf("expected (false,nil), got (%v,%v)", cont, err)
		}
	})

	t.Run("model error degrades to false", func(t *testing.T) {
		h := NewLLMMaxIterationsJudge(&LLMJudgeConfig{Model: &stubModel{err: errors.New("boom")}})
		cont, err := h(context.Background(), mkState())
		if err != nil || cont {
			t.Fatalf("expected (false,nil), got (%v,%v)", cont, err)
		}
	})

	t.Run("invalid json degrades to false", func(t *testing.T) {
		h := NewLLMMaxIterationsJudge(&LLMJudgeConfig{Model: &stubModel{resp: schema.AssistantMessage("not json", nil)}})
		cont, err := h(context.Background(), mkState())
		if err != nil || cont {
			t.Fatalf("expected (false,nil), got (%v,%v)", cont, err)
		}
	})

	t.Run("valid json continue", func(t *testing.T) {
		st := mkState()
		h := NewLLMMaxIterationsJudge(&LLMJudgeConfig{Model: &stubModel{
			resp: schema.AssistantMessage(`{"continue": true, "reason": "still"}`, nil),
		}})
		cont, err := h(context.Background(), st)
		if err != nil || !cont {
			t.Fatalf("expected (true,nil), got (%v,%v)", cont, err)
		}
		if got := len(st.NewMessage); got != 1 {
			t.Fatalf("NewMessage len=%d want 1", got)
		}
	})

	t.Run("valid json continue with direction appends user message", func(t *testing.T) {
		st := mkState()
		h := NewLLMMaxIterationsJudge(&LLMJudgeConfig{Model: &stubModel{
			resp: schema.AssistantMessage(`{"continue": true, "reason": "still", "direction": "先整理已有结果，再继续执行下一步"}`, nil),
		}})
		cont, err := h(context.Background(), st)
		if err != nil || !cont {
			t.Fatalf("expected (true,nil), got (%v,%v)", cont, err)
		}
		if got := len(st.NewMessage); got != 2 {
			t.Fatalf("NewMessage len=%d want 2", got)
		}
		msg := st.NewMessage[1]
		if msg.Role != schema.User || msg.Content != "先整理已有结果，再继续执行下一步" {
			t.Fatalf("unexpected direction message: role=%s content=%q", msg.Role, msg.Content)
		}
	})

	t.Run("stop with direction does not append", func(t *testing.T) {
		st := mkState()
		h := NewLLMMaxIterationsJudge(&LLMJudgeConfig{Model: &stubModel{
			resp: schema.AssistantMessage(`{"continue": false, "reason": "done", "direction": "继续"}`, nil),
		}})
		cont, err := h(context.Background(), st)
		if err != nil || cont {
			t.Fatalf("expected (false,nil), got (%v,%v)", cont, err)
		}
		if got := len(st.NewMessage); got != 1 {
			t.Fatalf("NewMessage len=%d want 1", got)
		}
	})
}

func TestShouldContinueAfterMax(t *testing.T) {
	lp := &looper{}
	mkCtx := func() *State {
		st := NewState("s", "r", schema.UserMessage("hi"), nil)
		st.ctx = context.Background()
		st.EventHandler = defaultEventCallback
		return st
	}

	t.Run("nil handler returns false", func(t *testing.T) {
		st := mkCtx()
		if lp.shouldContinueAfterMax(st) {
			t.Fatal("expected false when handler nil")
		}
	})

	t.Run("zero retry does not call handler", func(t *testing.T) {
		called := 0
		st := mkCtx()
		st.MaxIterationsHandler = func(ctx context.Context, state *State) (bool, error) {
			called++
			return true, nil
		}
		if lp.shouldContinueAfterMax(st) {
			t.Fatal("expected false when retry is zero")
		}
		if called != 0 {
			t.Fatalf("handler should not be called when retry is zero, called=%d", called)
		}
	})

	t.Run("retry limit honored", func(t *testing.T) {
		called := 0
		st := mkCtx()
		st.MaxIterationsHandler = func(ctx context.Context, state *State) (bool, error) {
			called++
			return true, nil
		}
		st.MaxIterationsRetry = 2
		st.Session[MaxIterationsRetryKey] = 2 // already at limit
		if lp.shouldContinueAfterMax(st) {
			t.Fatal("expected false at retry limit")
		}
		if called != 0 {
			t.Fatalf("handler should not be called at limit, called=%d", called)
		}
	})

	t.Run("handler decides", func(t *testing.T) {
		st := mkCtx()
		st.MaxIterationsRetry = 3
		st.MaxIterationsHandler = func(ctx context.Context, state *State) (bool, error) {
			return true, nil
		}
		if !lp.shouldContinueAfterMax(st) {
			t.Fatal("expected true")
		}
		st.MaxIterationsHandler = func(ctx context.Context, state *State) (bool, error) {
			return false, nil
		}
		if lp.shouldContinueAfterMax(st) {
			t.Fatal("expected false")
		}
	})

	t.Run("handler error treated as stop", func(t *testing.T) {
		st := mkCtx()
		st.MaxIterationsRetry = 3
		st.MaxIterationsHandler = func(ctx context.Context, state *State) (bool, error) {
			return true, errors.New("boom")
		}
		if lp.shouldContinueAfterMax(st) {
			t.Fatal("expected false on handler error")
		}
	})

	t.Run("bumpRetryCount", func(t *testing.T) {
		st := mkCtx()
		lp.bumpRetryCount(st)
		lp.bumpRetryCount(st)
		if got := getRetryCount(st); got != 2 {
			t.Fatalf("retry count=%d want 2", got)
		}
	})
}
