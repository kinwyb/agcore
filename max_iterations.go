package agcore

import "context"

// MaxIterationsRetryKey 已续跑次数在 State.Session 中的存储 key。
// 由 looper 在每次决定续跑时累加，插件实现也可读取该值用于决策。
const MaxIterationsRetryKey = "max_iterations_retry"

// MaxIterationsHandler agent 触发最大迭代次数限制时的插件回调。
//
// 返回 (true, nil)  : 续跑一轮；looper 不会修改 state.NewMessage / state.Input，
//
//	下一轮会把 state.FullMessages() 重新发给 runner。
//
// 返回 (false, nil) : 走默认终止路径，返回原"任务已达最大轮次"错误。
// 返回 (_, err)     : 视为终止，并把 err 向上抛。
type MaxIterationsHandler func(ctx context.Context, state *State) (bool, error)
