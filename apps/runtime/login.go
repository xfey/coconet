package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"time"

	"github.com/xfey/coconet/packages/contracts"
	"golang.org/x/oauth2"
)

func runLoginCommand(ctx context.Context, command string, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	server := flags.String("server", "", "Coconet server")
	noBrowser := flags.Bool("no-browser", false, "Print authorization link without opening a browser")
	if flags.Parse(args) != nil || flags.NArg() != 0 {
		return 2
	}
	paths, err := resolveRuntimePaths()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	var target deploymentTarget
	if *server != "" {
		target, err = discoverDeployment(ctx, *server)
	} else if _, binding, _, bindingErr := currentProjectBinding(); bindingErr == nil && binding != nil {
		target, err = deploymentTargetFromBinding(*binding)
	} else {
		target, err = discoverDeployment(ctx, environmentOr("COCONET_SERVER_URL", defaultServerURL))
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if command == "login" {
		_, err = loginToDeployment(ctx, paths, target, *noBrowser, stdout)
		if err == nil {
			if restartErr := resumeConnectedLocalDAGs(ctx, paths, target.DeploymentID); restartErr != nil {
				fmt.Fprintln(stderr, "Signed in; local Runtime restart is pending. Run coconet connect to retry:", restartErr)
			}
		}
	} else if command == "logout" {
		err = logoutDeployment(ctx, paths, target, stdout, stderr)
	} else {
		credential, found, loadErr := loadCredential(paths, target)
		if loadErr != nil {
			err = loadErr
		} else if !found {
			err = errors.New("You are not signed in. Run `coconet login`.")
		} else {
			client, clientErr := authenticatedClientForDeployment(ctx, target, credential.AccessToken)
			if clientErr != nil {
				err = clientErr
			} else {
				var account contracts.Account
				account, err = client.account(ctx)
				if err == nil {
					fmt.Fprintf(stdout, "%s\n  Server: %s\n", account.DisplayName, target.ServerURL)
				}
			}
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

type authorizedDevice struct {
	credential storedCredential
	projectID  string
	directory  *contracts.ConnectionDirectory
}

func loginToDeployment(ctx context.Context, paths runtimePaths, target deploymentTarget, noBrowser bool, output io.Writer) (storedCredential, error) {
	result, err := authorizeDeployment(ctx, paths, target, noBrowser, output, contracts.DeviceAuthorizationRequest{})
	return result.credential, err
}

func authorizeDeployment(ctx context.Context, paths runtimePaths, target deploymentTarget, noBrowser bool, output io.Writer, input contracts.DeviceAuthorizationRequest) (authorizedDevice, error) {
	return authorizeDeploymentWithChooser(ctx, paths, target, noBrowser, output, input, chooseConnectionFolder)
}

func authorizeDeploymentWithChooser(ctx context.Context, paths runtimePaths, target deploymentTarget, noBrowser bool, output io.Writer, input contracts.DeviceAuthorizationRequest, chooseFolder func(context.Context, string) (string, error)) (authorizedDevice, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	observed, err := discoverDeployment(ctx, target.ServerURL)
	if err != nil {
		return authorizedDevice{}, err
	}
	if !sameDeploymentTarget(target, observed) {
		return authorizedDevice{}, errors.New("Server identity changed; login was stopped")
	}
	if !slices.Contains(observed.Capabilities, contracts.DeploymentCapabilityAccountLogin) {
		return authorizedDevice{}, errors.New("This server must be upgraded to support account login")
	}
	if input.ConnectProject && !slices.Contains(observed.Capabilities, contracts.DeploymentCapabilityProjectSetup) || input.Directory != nil && !slices.Contains(observed.Capabilities, contracts.DeploymentCapabilityDirectorySetup) {
		return authorizedDevice{}, errors.New("This server must be upgraded to support browser folder connections. Ask its administrator to update Coconet Server")
	}
	client, err := newAPIClient(target.ServerURL, "")
	if err != nil {
		return authorizedDevice{}, err
	}
	verifier := oauth2.GenerateVerifier()
	name, _ := os.Hostname()
	if name == "" {
		name = runtime.GOOS
	}
	if len(name) > 100 {
		name = name[:100]
	}
	remote := noBrowser || remoteTerminal()
	input.DeviceName, input.CodeChallenge, input.CallbackURL = name, oauth2.S256ChallengeFromVerifier(verifier), ""
	var started contracts.DeviceAuthorization
	err = client.do(ctx, http.MethodPost, "/v1/auth/device", input, &started, false)
	if err != nil {
		return authorizedDevice{}, err
	}
	verification, parseErr := url.Parse(started.VerificationURI)
	var authorization string
	if parseErr == nil {
		values, err := url.ParseQuery(verification.Fragment)
		if err == nil && len(values) == 1 {
			authorization = values.Get("authorize")
		}
	}
	if parseErr != nil || verification.User != nil || verification.Host == "" || verification.Path != "/" || verification.RawQuery != "" || len(started.AuthorizationID) != 43 || authorization != started.AuthorizationID || verification.Scheme != "https" && !(verification.Scheme == "http" && (verification.Hostname() == "127.0.0.1" || verification.Hostname() == "localhost" || verification.Hostname() == "::1")) || started.DeviceCode == "" || started.Interval < 1 || started.Interval > 30 || started.ExpiresIn < 1 || started.ExpiresIn > 600 {
		return authorizedDevice{}, errors.New("Server returned an invalid authorization response")
	}
	// Verify an advertised browser origin independently when a CLI network override is used.
	if verification.Scheme+"://"+verification.Host != target.ServerURL {
		browserTarget, verifyErr := discoverDeployment(ctx, verification.Scheme+"://"+verification.Host)
		if verifyErr != nil || browserTarget.DeploymentID != target.DeploymentID || browserTarget.ServerPublicKeySHA256 != target.ServerPublicKeySHA256 {
			return authorizedDevice{}, errors.New("Could not verify the browser login server; use an endpoint reachable from both devices")
		}
	}
	retryCommand := "login"
	if input.ConnectProject {
		retryCommand = "connect"
	}
	folder := ""
	if input.Directory != nil {
		folder = input.Directory.Path
	}
	showDeviceAuthorization(ctx, output, started, name, folder, remote, openLoginBrowser)
	deadline := time.NewTimer(time.Duration(started.ExpiresIn) * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Duration(started.Interval) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return authorizedDevice{}, fmt.Errorf("Connection request cancelled or timed out. Run `coconet %s` to try again.", retryCommand)
		case <-deadline.C:
			return authorizedDevice{}, fmt.Errorf("Connection request expired. Run `coconet %s` to try again.", retryCommand)
		case <-ticker.C:
			var result contracts.DeviceAuthorization
			err = client.do(ctx, http.MethodPost, "/v1/auth/token", contracts.DeviceAuthorizationPoll{DeviceCode: started.DeviceCode, CodeVerifier: verifier}, &result, false)
			if err != nil {
				return authorizedDevice{}, err
			}
			if result.Status == contracts.StatusPending {
				if result.DirectoryRequestID != "" {
					if input.Directory == nil || !input.Directory.CanBrowse || remote {
						return authorizedDevice{}, errors.New("Server requested a folder chooser that this CLI did not enable")
					}
					fmt.Fprintln(output, "Choose a folder in the system window…")
					update := contracts.DeviceDirectoryUpdate{DeviceAuthorizationPoll: contracts.DeviceAuthorizationPoll{DeviceCode: started.DeviceCode, CodeVerifier: verifier}, RequestID: result.DirectoryRequestID}
					selected, chooseErr := chooseFolder(ctx, input.Directory.CWD)
					next := *input.Directory
					if errors.Is(chooseErr, errFolderSelectionCancelled) {
						update.Error = "cancelled"
					} else if chooseErr != nil {
						update.Error = "unavailable"
					} else {
						checked, _, existing, checkErr := connectionDirectory(paths, selected, noBrowser)
						if checkErr != nil {
							update.Error = "invalid"
						} else if existing != nil {
							update.Error = "already_connected"
						} else {
							update.CWD, update.Path = checked.CWD, checked.Path
							next.CWD, next.Path = checked.CWD, checked.Path
						}
					}
					if err := client.do(ctx, http.MethodPost, "/v1/auth/directory", update, nil, false); err != nil {
						return authorizedDevice{}, err
					}
					next.Revision++
					input.Directory = &next
					if update.Error == "" {
						fmt.Fprintf(output, "Folder: %s\nReview it in your browser to connect.\n", next.Path)
					} else {
						fmt.Fprintln(output, "Folder unchanged. Return to your browser to continue or try again.")
					}
				}
				continue
			}
			if result.Status != contracts.StatusOK || result.Account == nil || result.Account.AccountID == "" || len(result.AccessToken) < 32 || input.ConnectProject && result.ProjectID == "" {
				return authorizedDevice{}, errors.New("Server did not return a valid device credential")
			}
			if input.Directory != nil {
				var validationErr error
				if result.Directory == nil || *result.Directory != *input.Directory {
					validationErr = errors.New("The confirmed directory does not match this CLI. Run `coconet connect` again")
				} else {
					checked, _, existing, checkErr := connectionDirectory(paths, result.Directory.CWD, noBrowser)
					if checkErr != nil || existing != nil || checked.CWD != result.Directory.CWD || checked.Path != result.Directory.Path {
						validationErr = errors.New("The selected folder changed or is already connected. Run `coconet connect` again")
					}
				}
				if validationErr != nil {
					client.token = result.AccessToken
					_ = client.do(ctx, http.MethodPost, "/v1/auth/logout", map[string]string{}, nil, true)
					return authorizedDevice{}, validationErr
				}
			}
			credential, saveErr := persistLogin(ctx, paths, target, *result.Account, result.AccessToken)
			if saveErr != nil {
				client.token = result.AccessToken
				_ = client.do(ctx, http.MethodPost, "/v1/auth/logout", map[string]string{}, nil, true)
				return authorizedDevice{}, saveErr
			}
			if !input.ConnectProject {
				writeCLISuccess(output, "Signed in as "+result.Account.DisplayName)
			}
			return authorizedDevice{credential: credential, projectID: result.ProjectID, directory: result.Directory}, nil
		}
	}
}

func showDeviceAuthorization(ctx context.Context, output io.Writer, started contracts.DeviceAuthorization, device, folder string, remote bool, openBrowser func(context.Context, string) error) {
	fmt.Fprintf(output, "Connection Information\n  Device: %s\n", device)
	if folder != "" {
		fmt.Fprintf(output, "  Folder: %s\n", folder)
	}
	fmt.Fprintln(output, "Browser Confirmation")
	if !remote {
		fmt.Fprintln(output, "  Opening your browser...")
		browserCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := openBrowser(browserCtx, started.VerificationURI)
		cancel()
		if err != nil {
			fmt.Fprintln(output, "  Could not open your browser. Open the link below.")
		}
	} else {
		fmt.Fprintln(output, "  Open this link in your browser:")
	}
	fmt.Fprintf(output, "  %s\n", started.VerificationURI)
}

func openLoginBrowser(ctx context.Context, address string) error {
	program := "xdg-open"
	if runtime.GOOS == "darwin" {
		program = "open"
	}
	command := exec.CommandContext(ctx, program, address)
	return command.Run()
}

func persistLogin(ctx context.Context, paths runtimePaths, target deploymentTarget, account contracts.Account, token string) (storedCredential, error) {
	release, err := acquireAccountLock(ctx, paths, target.DeploymentID)
	if err != nil {
		return storedCredential{}, err
	}
	defer release()
	bindings, err := loadBindingState(paths)
	if err != nil {
		return storedCredential{}, err
	}
	for _, binding := range bindings.Bindings {
		if binding.DeploymentID == target.DeploymentID && binding.AccountID != account.AccountID {
			return storedCredential{}, errors.New("Some local folders are connected under another account. Disconnect those folders before switching accounts; their sessions will be retained.")
		}
	}
	previous, hadPrevious, err := loadCredential(paths, target)
	if err != nil {
		return storedCredential{}, err
	}
	if err := saveCredential(paths, target, account, token); err != nil {
		return storedCredential{}, err
	}
	if hadPrevious && previous.AccessToken != token {
		if oldClient, clientErr := authenticatedClientForDeployment(ctx, target, previous.AccessToken); clientErr == nil {
			_ = oldClient.do(ctx, http.MethodPost, "/v1/auth/logout", map[string]string{}, nil, true)
		}
	}
	credential, _, err := loadCredential(paths, target)
	return credential, err
}

func logoutDeployment(ctx context.Context, paths runtimePaths, target deploymentTarget, stdout, stderr io.Writer) error {
	if err := stopDeploymentLocalDAGs(ctx, paths, target.DeploymentID); err != nil {
		return err
	}
	release, err := acquireAccountLock(ctx, paths, target.DeploymentID)
	if err != nil {
		return err
	}
	defer release()
	credential, found, err := loadCredential(paths, target)
	if err != nil {
		return err
	}
	if !found {
		fmt.Fprintln(stdout, "You are already signed out.")
		return nil
	}
	client, err := authenticatedClientForDeployment(ctx, target, credential.AccessToken)
	if err == nil {
		err = client.do(ctx, http.MethodPost, "/v1/auth/logout", map[string]string{}, nil, true)
	}
	if err != nil {
		fmt.Fprintln(stderr, "The server could not revoke this device. Remove it in the Dashboard account settings when the server is reachable.")
	}
	state, err := loadCredentialStateForWrite(paths)
	if err != nil {
		return err
	}
	state.Credentials = slices.DeleteFunc(state.Credentials, func(c storedCredential) bool { return c.DeploymentID == target.DeploymentID })
	if err := atomicWriteJSON(paths.credentialPath, state, 0o600); err != nil {
		return err
	}
	if credential.TokenStorage == "keychain" {
		if err := exec.Command("security", "delete-generic-password", "-s", keychainService, "-a", keychainAccount(credential)).Run(); err != nil {
			fmt.Fprintln(stderr, "Could not remove this device credential from macOS Keychain.")
		}
	}
	writeCLISuccess(stdout, "Signed out on this device")
	fmt.Fprintln(stdout, "  Project connections and pending uploads are retained. Sign in again to resume syncing.")
	return nil
}
