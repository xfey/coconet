package claude

import (
	"context"
	"github.com/xfey/coconet/adapters"
)

func (adapter Adapter) ImportText(ctx context.Context, request adapters.TextImportRequest) (adapters.ForkResult, error) {
	native, cleanup, err := adapters.PrepareClaudeText(ctx, request)
	if err != nil {
		return adapters.ForkResult{}, err
	}
	defer cleanup()
	return adapter.Fork(ctx, native)
}
