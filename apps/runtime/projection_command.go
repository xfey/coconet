package main

import (
	"context"
	"fmt"
	"io"
)

func runProjectionCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	project, managed, err := resolveLocalMCPProject()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if !managed {
		fmt.Fprintln(stderr, "Run coconet connect in this folder first.")
		return 1
	}
	return runLocalProjectionCommand(ctx, project, args, stdout, stderr)
}
