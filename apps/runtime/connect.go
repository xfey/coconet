package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/xfey/coconet/packages/cliui"
	"github.com/xfey/coconet/packages/contracts"
	"github.com/xfey/coconet/packages/projectlink"
)

const defaultServerURL = projectlink.HostedEndpoint

// init is a compatibility spelling of the same connection workflow.
func runInit(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return runConnect(ctx, args, stdin, stdout, stderr)
}

func runConnect(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	options, err := parseConnectionOptions(args)
	if err != nil {
		fmt.Fprintln(stderr, "usage: coconet connect [PROJECT_ID] [--path DIRECTORY] [--project ID | --new] [--server URL] [--no-browser] [--verbose]")
		return 2
	}
	if options.project != "" {
		if _, ok := projectlink.ProjectUID(options.project); !ok {
			fmt.Fprintln(stderr, "Invalid Project ID. Copy the Project ID from Share and try again.")
			return 1
		}
	}
	paths, err := resolveRuntimePaths()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	directory, repository, existing, err := connectionDirectory(paths, options.path, options.noBrowser)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if existing != nil {
		if options.server != "" {
			options.server, err = normalizeServerURL(options.server)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
		}
		if options.project != "" && options.project != existing.ProjectID || options.server != "" && options.server != existing.LocalServerURL {
			fmt.Fprintln(stderr, "This folder is already connected to a project. Run `coconet disconnect` in that folder before connecting elsewhere.")
			return 1
		}
		target, targetErr := deploymentTargetFromBinding(*existing)
		if targetErr != nil {
			fmt.Fprintln(stderr, targetErr)
			return 1
		}
		_, client, err := clientForBinding(ctx, paths, *existing, target)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		access, err := client.projectAccess(ctx, existing.ProjectID)
		if err != nil {
			printProjectAccessError(stderr, err)
			return 1
		}
		// An archive can still be inspected, but must not receive new uploads.
		if access.Status == contracts.StatusProjectArchived || access.Project != nil && access.Project.Status == contracts.ProjectArchived {
			return printProjectStatusAt(ctx, repository.root, options.verbose, "Already connected to", stdout, stderr)
		}
		if err := validateConnectedAccess(access, existing.AccountID, existing.ProjectID); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if existing.Workflow != localDAGWorkflow {
			// Refresh the signed capability while preserving the pinned server,
			// account, project, directory and connection time. Never import old
			// history or reset device credentials as an upgrade side effect.
			fresh, err := discoverDeployment(ctx, target.ServerURL)
			if err != nil || fresh.DeploymentID != target.DeploymentID || fresh.ServerPublicKeySHA256 != target.ServerPublicKeySHA256 {
				fmt.Fprintln(stderr, "Could not verify the connected Relay identity; the existing association is unchanged.")
				return 1
			}
			if err := persistBinding(paths, existing.CollaborationRoot, fresh, existing.AccountID, existing.ProjectID, existing.ConnectedAt); err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			existing.Workflow = localDAGWorkflow
		}
		if err := finishLocalDAGConnection(localDAGBindingRoot(paths, *existing), stdout); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := clearPendingInit(paths, repository.root, existing.DeploymentID, existing.AccountID); err != nil {
			fmt.Fprintln(stderr, "Project is connected, but setup recovery data could not be cleared:", err)
			return 1
		}
		writeCLISuccess(stdout, "Already connected to "+access.Project.Name)
		return 0
	}
	serverURL := options.server
	if serverURL == "" {
		serverURL, err = defaultServer(paths)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	target, err := discoverDeployment(ctx, serverURL)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if !slices.Contains(target.Capabilities, contracts.DeploymentCapabilityLocalDAG) {
		fmt.Fprintln(stderr, "This server must be upgraded to the Coconet 0.20 Relay before connecting.")
		return 1
	}
	var credential storedCredential
	var client *apiClient
	var access contracts.ProjectAccess
	expectedProjectID := options.project
	if options.newProject {
		credential, client, err = ensureAccount(ctx, paths, target, options.noBrowser, stdout)
		if err == nil {
			var requestID string
			requestID, err = pendingInitRequestID(paths, repository.root, target.DeploymentID, credential.Account.AccountID)
			if err == nil {
				access, err = client.createProject(ctx, filepath.Base(repository.root), requestID)
			}
		}
	} else {
		input := contracts.DeviceAuthorizationRequest{ConnectProject: true, Directory: &directory, SuggestedProjectID: options.project}
		if name := filepath.Base(repository.root); utf8.ValidString(name) && utf8.RuneCountInString(name) <= 120 {
			input.ProjectName = name
		}
		bindings, loadErr := loadBindingState(paths)
		if loadErr != nil {
			fmt.Fprintln(stderr, loadErr)
			return 1
		}
		for _, binding := range bindings.Bindings {
			if binding.DeploymentID == target.DeploymentID {
				input.ExpectedAccountID = binding.AccountID
				break
			}
		}
		var authorized authorizedDevice
		authorized, err = authorizeDeployment(ctx, paths, target, options.noBrowser, stdout, input)
		if err == nil {
			credential = authorized.credential
			directory = *authorized.directory
			_, repository, existing, err = connectionDirectory(paths, directory.CWD, options.noBrowser)
			if err != nil || existing != nil || repository.root != directory.Path {
				fmt.Fprintln(stderr, "The selected folder changed or was connected elsewhere. Run `coconet connect` again.")
				return 1
			}
			expectedProjectID = authorized.projectID
			client, err = authenticatedClientForDeployment(ctx, target, credential.AccessToken)
			if err == nil {
				access, err = client.projectAccess(ctx, authorized.projectID)
			}
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := validateConnectedAccess(access, credential.Account.AccountID, expectedProjectID); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := persistBinding(paths, repository.root, target, credential.Account.AccountID, access.Project.ProjectID, ""); err != nil {
		fmt.Fprintf(stderr, "Project %s was selected, but its local connection could not be saved: %v\n", access.Project.ProjectID, err)
		return 1
	}
	if err := reportWorkspaceSnapshot(ctx, paths, target, true, client); err != nil {
		cliui.Warning(stderr, "Connected locally; the Dashboard folder list will update on the next connect, status or sync.")
	}
	if binding, err := bindingAtRoot(paths, repository.root); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	} else if binding != nil && binding.Workflow == localDAGWorkflow {
		if err := finishLocalDAGConnection(localDAGBindingRoot(paths, *binding), stdout); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	} else {
		fmt.Fprintln(stderr, "The local connection is incomplete; run coconet connect again.")
		return 1
	}
	if err := clearPendingInit(paths, repository.root, target.DeploymentID, credential.Account.AccountID); err != nil {
		fmt.Fprintf(stderr, "Project is connected, but setup recovery data could not be cleared: %v\n", err)
		return 1
	}
	heading := "Connected to"
	if options.newProject {
		heading = "Created and connected to"
	}
	if options.verbose {
		printConnectionStatus(ctx, repository.root, true, heading, stdout, stderr)
	} else {
		writeCLISuccess(stdout, heading+" "+access.Project.Name)
	}
	return 0
}

func printConnectionStatus(ctx context.Context, root string, verbose bool, heading string, stdout, stderr io.Writer) {
	if printProjectStatusAt(ctx, root, verbose, heading, stdout, stderr) != 0 {
		// The Project and binding already exist. A failed status request
		// must not report the completed initialization as a failure.
		cliui.Warning(stderr, "Project is connected, but its status is unavailable.")
		fmt.Fprintln(stderr, "Run `coconet status` to retry.")
	}
}

func runProjectStatus(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	verbose := flags.Bool("verbose", false, "show connection details")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: coconet status [--verbose]")
		return 2
	}
	return printProjectStatus(ctx, *verbose, "Connected to", stdout, stderr)
}

func printProjectStatus(ctx context.Context, verbose bool, heading string, stdout, stderr io.Writer) int {
	return printProjectStatusAt(ctx, currentDirectory(), verbose, heading, stdout, stderr)
}

func printProjectStatusAt(ctx context.Context, cwd string, verbose bool, heading string, stdout, stderr io.Writer) int {
	paths, err := resolveRuntimePaths()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	repository, binding, err := collaborationRootAt(paths, cwd)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if binding == nil {
		fmt.Fprintln(stdout, "This folder is not connected to Coconet.\n\nConnect this folder to a project:\n  coconet connect")
		return 0
	}
	target, err := deploymentTargetFromBinding(*binding)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	_, client, err := clientForBinding(ctx, paths, *binding, target)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	access, err := client.projectAccess(ctx, binding.ProjectID)
	if err != nil {
		printProjectAccessError(stderr, err)
		return 1
	}
	if access.Project == nil || access.Project.ProjectID != binding.ProjectID || access.Membership == nil || access.Membership.ProjectID != binding.ProjectID || access.Membership.Account.AccountID != binding.AccountID {
		fmt.Fprintln(stderr, "This folder no longer has a valid project connection. Ask a teammate for a new invitation.")
		return 1
	}
	archived := access.Status == contracts.StatusProjectArchived || access.Project.Status == contracts.ProjectArchived
	if !archived && (access.Status != contracts.StatusOK || access.Membership.Status != contracts.MembershipActive) {
		fmt.Fprintln(stderr, "You no longer have active access to this project.")
		return 1
	}
	if archived {
		fmt.Fprintf(stdout, "%s · read-only archive\n", cliui.Bold(stdout, access.Project.Name))
	} else {
		writeCLISuccess(stdout, heading+" "+cliui.Bold(stdout, access.Project.Name))
	}
	if err := reportWorkspaceSnapshot(ctx, paths, target, true, client); err != nil {
		cliui.Warning(stderr, "Could not update the Dashboard folder list; it will retry on the next connect, status or sync.")
	}
	// Report the active workflow without opening retired upload databases.
	if binding.Workflow == localDAGWorkflow {
		config, configErr := loadLocalDAGService(localDAGBindingRoot(paths, *binding))
		if configErr != nil {
			cliui.Warning(stdout, "Local Runtime configuration: "+configErr.Error())
		} else if config.PauseReason != "" {
			cliui.Note(stdout, config.PauseReason)
		} else {
			cliui.Note(stdout, "Local DAG processing: "+config.Provider+" / "+config.Model+". Use coconet projection status for device processing / synchronization and coconet diagnostics list for run logs.")
		}
	} else {
		cliui.Warning(stdout, "This connection needs the local Runtime workflow. Run `coconet connect` to enable automatic processing with your Agent. Existing history has not been imported.")
	}
	fields := []cliField{{Label: "Account", Value: access.Membership.Account.DisplayName}, {Label: "Project ID", Value: binding.ProjectID}}
	if verbose {
		fields = append(fields, []cliField{
			{Label: "Workspace", Value: repository.root},
			{Label: "Server", Value: binding.LocalServerURL},
			{Label: "Deployment", Value: binding.DeploymentID},
		}...)
	} else if !isHostedServer(binding.LocalServerURL) {
		fields = append(fields, cliField{Label: "Server", Value: binding.LocalServerURL})
	}
	if len(fields) > 0 {
		fmt.Fprintln(stdout)
		writeCLIFields(stdout, fields...)
	}
	fmt.Fprintf(stdout, "\nManage this project in your browser:\n  %s/%s\n", target.ServerURL, binding.ProjectID)

	return 0
}

func printProjectAccessError(output io.Writer, err error) {
	var remote *serverError
	if errors.As(err, &remote) && remote.HTTPStatus == http.StatusNotFound && remote.Envelope.Code == "project_not_found" {
		fmt.Fprintln(output, "This folder's project has been deleted. Your account is unchanged. In this folder, run `coconet disconnect`, then `coconet connect` to choose or create a project. Local conversation history is preserved.")
		return
	}
	fmt.Fprintln(output, err)
}

func runDisconnect(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return runDisconnectWithBindingRemoval(ctx, args, stdout, stderr, detachFolderBinding)
}

func runDisconnectWithBindingRemoval(ctx context.Context, args []string, stdout, stderr io.Writer, removeBinding func(runtimePaths, string) error) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "usage: coconet disconnect")
		return 2
	}
	_, binding, paths, err := currentProjectBinding()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if binding == nil {
		fmt.Fprintln(stdout, "This folder is not connected to Coconet.")
		return 0
	}
	release, err := acquireAccountLock(ctx, paths, binding.DeploymentID)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer release()
	if binding.Workflow == localDAGWorkflow {
		if err := stopConnectedLocalDAG(ctx, localDAGBindingRoot(paths, *binding)); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	if err := removeBinding(paths, binding.CollaborationRoot); err != nil {
		fmt.Fprintln(stderr, "Could not disconnect this folder:", err)
		return 1
	}
	writeCLISuccess(stdout, "Disconnected this folder")
	if target, err := deploymentTargetFromBinding(*binding); err == nil {
		if err := reportWorkspaceSnapshot(ctx, paths, target, true, nil); err != nil {
			cliui.Warning(stderr, "Disconnected locally; the Dashboard folder list will update when this device reconnects.")
		}
	}
	fmt.Fprintln(stdout, "  Your project membership and other devices are unchanged.")
	return 0
}

func detachFolderBinding(paths runtimePaths, root string) error {
	_, err := removeBindingAtRoot(paths, root)
	return err
}

func currentProjectBinding() (repository, *contracts.LocalBinding, runtimePaths, error) {
	paths, err := resolveRuntimePaths()
	if err != nil {
		return repository{}, nil, runtimePaths{}, err
	}
	repository, binding, err := currentCollaborationRoot(paths)
	return repository, binding, paths, err
}

func currentCollaborationRoot(paths runtimePaths) (repository, *contracts.LocalBinding, error) {
	return collaborationRootAt(paths, currentDirectory())
}

func collaborationRootAt(paths runtimePaths, cwd string) (repository, *contracts.LocalBinding, error) {
	binding, err := bindingForAnyCWD(paths, cwd)
	if err != nil {
		return repository{}, nil, err
	}
	if binding != nil {
		root := repository{root: binding.CollaborationRoot}
		if gitRepository, gitErr := discoverGitRepository(cwd); gitErr == nil && samePath(gitRepository.root, binding.CollaborationRoot) {
			root.isGit = true
		}
		return root, binding, nil
	}
	root, err := discoverCollaborationRoot(cwd)
	return root, nil, err
}

func deploymentTargetFromBinding(binding contracts.LocalBinding) (deploymentTarget, error) {
	target := deploymentTarget{
		DeploymentID: binding.DeploymentID, ServerURL: binding.LocalServerURL,
		ServerPublicKeySHA256: binding.ServerPublicKeySHA256,
	}
	if err := validateDeploymentTarget(target); err != nil {
		return deploymentTarget{}, err
	}
	return target, nil
}

func clientForBinding(ctx context.Context, paths runtimePaths, binding contracts.LocalBinding, target deploymentTarget) (storedCredential, *apiClient, error) {
	credential, exists, err := loadCredential(paths, target)
	if err != nil {
		return storedCredential{}, nil, err
	}
	if !exists || credential.Account.AccountID != binding.AccountID {
		return storedCredential{}, nil, errors.New("Sign-in credentials for this project are unavailable. Run `coconet login` on this device")
	}
	client, err := authenticatedClientForDeployment(ctx, target, credential.AccessToken)
	if err != nil {
		return storedCredential{}, nil, err
	}
	return credential, client, nil
}

func validateConnectedAccess(access contracts.ProjectAccess, accountID, expectedProjectID string) error {
	if access.Status != contracts.StatusOK || access.Project == nil || access.Membership == nil ||
		access.Membership.Status != contracts.MembershipActive || access.Membership.Account.AccountID != accountID ||
		access.Membership.ProjectID != access.Project.ProjectID ||
		(expectedProjectID != "" && access.Project.ProjectID != expectedProjectID) {
		return errors.New("The server did not confirm active access to this project")
	}
	return nil
}

func ensureAccount(ctx context.Context, paths runtimePaths, target deploymentTarget, noBrowser bool, stdout io.Writer) (storedCredential, *apiClient, error) {
	credential, exists, err := loadCredential(paths, target)
	if err != nil {
		return storedCredential{}, nil, err
	}
	if exists {
		client, err := authenticatedClientForDeployment(ctx, target, credential.AccessToken)
		if err != nil {
			return storedCredential{}, nil, err
		}
		account, accountErr := client.account(ctx)
		if accountErr == nil {
			credential.Account = account
			return credential, client, nil
		}
		var remoteError *serverError
		if !errors.As(accountErr, &remoteError) || remoteError.HTTPStatus != 401 {
			return storedCredential{}, nil, accountErr
		}
	}

	credential, err = loginToDeployment(ctx, paths, target, noBrowser, stdout)
	if err != nil {
		return storedCredential{}, nil, err
	}
	client, err := authenticatedClientForDeployment(ctx, target, credential.AccessToken)
	return credential, client, err
}

func persistBinding(paths runtimePaths, root string, target deploymentTarget, accountID, projectID, connectedAt string) error {
	if connectedAt == "" {
		connectedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if !slices.Contains(target.Capabilities, contracts.DeploymentCapabilityLocalDAG) {
		return errors.New("this server must be upgraded to the Coconet 0.20 Relay before connecting")
	}
	if _, err := loadLocalDAGIdentity(paths, true); err != nil {
		return err
	}
	return saveBinding(paths, contracts.LocalBinding{Workflow: localDAGWorkflow,
		SchemaVersion: contracts.LocalBindingSchemaVersion, DeploymentID: target.DeploymentID,
		ServerPublicKeySHA256: target.ServerPublicKeySHA256, ProjectID: projectID, AccountID: accountID,
		CollaborationRoot: root, LocalServerURL: target.ServerURL, ConnectedAt: connectedAt,
	})
}

func currentDirectory() string {
	if value := os.Getenv("COCONET_CWD"); value != "" {
		return value
	}
	value, err := os.Getwd()
	if err != nil {
		return "."
	}
	return value
}
