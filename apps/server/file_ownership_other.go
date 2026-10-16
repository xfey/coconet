//go:build !unix

package main

func preserveExistingFileOwnership(_, _ string) error {
	return nil
}
