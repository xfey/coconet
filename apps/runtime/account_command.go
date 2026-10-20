package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/xfey/coconet/packages/contracts"
)

type pendingAccountKey struct {
	DeploymentID string `json:"deployment_id"`
	AccountID    string `json:"account_id"`
	NewKey       string `json:"new_key"`
}

// Explicit choices override the current folder; the saved default is only used
// without a binding. Unreadable bindings must never redirect account operations.
func selectedDeployment(ctx context.Context, paths runtimePaths, server string) (deploymentTarget, error) {
	if server != "" {
		return discoverDeployment(ctx, server)
	}
	if binding, err := bindingForAnyCWD(paths, currentDirectory()); err != nil {
		return deploymentTarget{}, err
	} else if binding != nil {
		return deploymentTargetFromBinding(*binding)
	}
	endpoint, err := defaultServer(paths)
	if err != nil {
		return deploymentTarget{}, err
	}
	return discoverDeployment(ctx, endpoint)
}

func runAccount(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(ctx, mcpHandoffTimeout)
	defer cancel()
	if len(args) == 0 || (args[0] != "key" && args[0] != "reset-key") {
		fmt.Fprintln(stderr, "usage: coconet account <key|reset-key> [--server URL]")
		return 2
	}
	flags := flag.NewFlagSet("account", flag.ContinueOnError)
	flags.SetOutput(stderr)
	server := flags.String("server", "", "Deployment endpoint")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 {
		return 2
	}
	paths, err := resolveRuntimePaths()
	if err != nil {
		fmt.Fprintln(stderr, "Could not read the local Coconet configuration.")
		return 1
	}
	target, err := selectedDeployment(ctx, paths, *server)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	// Hold the same Deployment drain lock as sync, including credential persistence.
	release, err := acquireAccountLock(ctx, paths, target.DeploymentID)
	if err != nil {
		fmt.Fprintln(stderr, "Could not lock the local account data. Wait for the current operation to finish and retry.")
		return 1
	}
	defer release()
	credential, found, err := loadCredential(paths, target)
	if err != nil || !found {
		fmt.Fprintln(stderr, "No account key was found for this server. Run `coconet connect` first.")
		return 1
	}
	pendingPath := filepath.Join(filepath.Dir(deploymentSyncPath(paths, target.DeploymentID)), "account-key-reset.json")
	if args[0] == "key" {
		if _, err := os.Stat(pendingPath); err == nil || !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(stderr, "Account key reset is incomplete. Run `coconet account reset-key` to finish it.")
			return 1
		}
		// This explicit credential command is the only ordinary CLI output containing the key.
		fmt.Fprintln(stdout, credential.AccessToken)
		return 0
	}
	if err := resetLocalAccountKey(ctx, paths, target, credential, pendingPath); err != nil {
		fmt.Fprintln(stderr, "Account key reset is incomplete. Check the network and local storage, then run the same command to resume.")
		return 1
	}
	writeCLISuccess(stdout, "Account key replaced")
	fmt.Fprintln(stdout, "  This device now uses the new key. Other devices and browser sessions were signed out.\n\nShow your new key:\n  coconet account key")
	return 0
}

func resetLocalAccountKey(ctx context.Context, paths runtimePaths, target deploymentTarget, credential storedCredential, pendingPath string) error {
	content, err := os.ReadFile(pendingPath)
	pending := pendingAccountKey{DeploymentID: target.DeploymentID, AccountID: credential.Account.AccountID}
	recovering := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if recovering {
		if err := json.Unmarshal(content, &pending); err != nil {
			return errors.New("invalid pending key reset")
		}
		decoded, err := base64.RawURLEncoding.DecodeString(pending.NewKey)
		if err != nil || len(decoded) != 32 || pending.DeploymentID != target.DeploymentID || pending.AccountID != credential.Account.AccountID {
			return errors.New("pending key reset identity differs")
		}
		if err := os.Chmod(pendingPath, 0o600); err != nil {
			return err
		}
	} else {
		var random [32]byte
		if _, err := rand.Read(random[:]); err != nil {
			return err
		}
		pending.NewKey = base64.RawURLEncoding.EncodeToString(random[:])
		if err := atomicWriteJSON(pendingPath, pending, 0o600); err != nil {
			return err
		}
	}
	keys := []string{credential.AccessToken}
	if recovering {
		keys = []string{pending.NewKey, credential.AccessToken}
	}
	var result contracts.ResetAccountKeyResponse
	for i, key := range keys {
		client, err := authenticatedClientForDeployment(ctx, target, key)
		if err != nil {
			return err
		}
		request, err := client.newRequest(ctx, http.MethodPut, "/v1/account/key", contracts.ResetAccountKeyRequest{SchemaVersion: 1, NewKey: pending.NewKey}, true)
		if err != nil {
			return err
		}
		err = client.send(request, &result)
		if err == nil {
			break
		}
		var remote *serverError
		if i+1 < len(keys) && errors.As(err, &remote) && remote.HTTPStatus == 401 {
			continue
		}
		return err
	}
	if result.SchemaVersion != 1 || result.Status != contracts.StatusOK || result.AccountID != credential.Account.AccountID {
		return errors.New("key reset response identity differs")
	}
	if err := saveCredential(paths, target, credential.Account, pending.NewKey); err != nil {
		return err
	}
	return os.Remove(pendingPath)
}

func shellCommand(program string, args []string) string {
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
	parts := []string{quote(program)}
	for _, arg := range args {
		parts = append(parts, quote(arg))
	}
	return strings.Join(parts, " ")
}
