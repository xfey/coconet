package main

import (
	"time"

	"github.com/xfey/coconet/adapters"
	"github.com/xfey/coconet/adapters/claude"
	"github.com/xfey/coconet/adapters/codex"
	"github.com/xfey/coconet/adapters/cursor"
)

func runtimeSessionAdapters() []adapters.Adapter {
	return []adapters.Adapter{
		codex.New(environmentOr("COCONET_CODEX_COMMAND", codex.DefaultCommand)),
		claude.New(environmentOr("COCONET_CLAUDE_COMMAND", claude.DefaultCommand)),
		cursor.New(environmentOr("COCONET_CURSOR_COMMAND", cursor.DefaultCommand)),
	}
}

func displayTimestamp(value string) string {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return value
	}
	return parsed.Local().Format("2006-01-02 15:04 MST")
}
