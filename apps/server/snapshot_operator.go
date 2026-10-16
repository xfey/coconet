package main

import (
	"flag"
	"fmt"
	"io"
)

func runSnapshotCompact(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("compact", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "Server configuration")
	output := flags.String("output", "", "new compacted database path")
	if flags.Parse(args) != nil || flags.NArg() != 0 || *configPath == "" || *output == "" {
		return 2
	}
	config, err := loadServerConfigReadOnly(*configPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if config.MetadataDriver != "bbolt" {
		fmt.Fprintln(stderr, "compact supports bbolt only")
		return 1
	}
	if err = compactBbolt(config.DataPath, *output); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, "Compacted metadata written to", *output, "; the original database remains in place.")
	return 0
}
