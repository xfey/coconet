package main

import (
	"context"
	"fmt"
	"io"

	"github.com/xfey/coconet/adapters"
)

func runLibraryCommand(ctx context.Context, args []string, stdout, stderr io.Writer, configured []adapters.Adapter) int {
	project, managed, err := resolveLocalMCPProject()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if !managed {
		fmt.Fprintln(stderr, "Run coconet connect in this folder first.")
		return 1
	}
	return runLocalLibraryCommand(ctx, project, args, stdout, stderr)
}
