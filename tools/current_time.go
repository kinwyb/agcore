package tools

import (
	"context"
	"time"

	"github.com/kinwyb/agcore/types"
)

// NewGetCurrentTimeTool 创建获取当前时间的工具
func NewGetCurrentTimeTool() *types.BaseTool {
	return types.NewBaseTool(
		"get_current_time",
		"Get the current date and time, including the day of the week and timezone. Call this tool when you need to know the current time, date, or day of week.",
		getEmptyParameters(),
		func(ctx context.Context, params map[string]interface{}) (string, error) {
			return time.Now().Format("2006-01-02 Monday 15:04:05 MST"), nil
		},
	)
}
