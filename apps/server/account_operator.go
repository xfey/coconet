package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/xfey/coconet/packages/contracts"
)

// Local operator access creates an account in key mode. No public signup endpoint
// accepts unverified profile fields, and the generated key is only printed here.
func runServerAccount(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "create" {
		fmt.Fprintln(stderr, "usage: coconet-server account create --config PATH --name NAME")
		return 2
	}
	flags := flag.NewFlagSet("account create", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "Server configuration")
	name := flags.String("name", "", "User display name")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || *configPath == "" || !validDisplayName(strings.TrimSpace(*name)) {
		return 2
	}
	_, _, storage, _, err := openConfiguredServer(ctx, *configPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer storage.close()
	identity, err := newAuthority(storage).bootstrapAccount(contracts.AccountBootstrapRequest{SchemaVersion: 1, DisplayName: *name})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stderr, "Account created for %s. Keep the following Account Key private.\n", identity.Account.DisplayName)
	fmt.Fprintln(stdout, identity.AccessToken)
	return 0
}
