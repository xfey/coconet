package clihelp

func Runtime() Command {
	json := boolean("json", "Write machine-readable JSON")
	server := option("server", "URL", "", "Server URL, optionally with a path prefix; otherwise use the connected folder's server, the saved default, or https://api.coconet.space. Explicit http:// endpoints need no environment setup")
	root := option("root", "DIR", "", "Absolute private Runtime store; otherwise use the connected folder")
	project := option("project", "ID", "", "Project ID when selecting an explicit Runtime store")
	fixed := []Option{
		option("node", "ID", "", "Required for Library edits; optional for reading or continuing a synchronized source"),
		option("session", "ID", "", "Required for a fixed source: use the selected Session ID"),
		option("version", "N", "0", "Required for a fixed source: use the selected positive version number"),
	}
	request := option("request-id", "ID", "", "Generated if omitted; on retry reuse the printed ID and all original arguments")
	connect := Command{Name: "connect", Summary: "Connect this folder; choose or create a project in your browser", Usage: "[PROJECT_ID] [options]", Options: []Option{
		option("path", "DIRECTORY", "", "Folder to connect; defaults to the current directory (Git subdirectories use the repository root)"),
		option("project", "ID", "", "Preselect a coco_ project ID; equivalent to positional PROJECT_ID"),
		server,
		boolean("new", "Create a new project directly; mutually exclusive with a project ID"),
		boolean("no-browser", "Print the connection link without opening a browser; also automatic over SSH"),
		boolean("verbose", "Show account, connection and local processing details"),
	}, Notes: []string{"A connected folder remembers its server. Disconnect before changing its project or server. Existing history is not imported automatically.", "Supply PROJECT_ID before flags, or after all flags; flags after a trailing PROJECT_ID are not parsed. Browser sign-in may still be required with --new."}, Examples: []string{"coconet connect", "coconet connect coco_PROJECT_ID --server https://coconet.example.com --no-browser", "coconet connect --path '/path/to/work folder'"}}
	init := connect
	init.Name, init.Summary = "init", "Alias for connect"
	init.Examples = []string{"coconet init --no-browser"}
	login := Command{Name: "login", Summary: "Sign in on this device", Usage: "[options]", Options: []Option{server, boolean("no-browser", "Print the sign-in link without opening a browser; only used by login")}, Examples: []string{"coconet login --no-browser"}}
	logout, whoami := login, login
	logout.Name, logout.Summary, logout.Examples = "logout", "Sign out this device; keep folder connections and pending work", []string{"coconet logout"}
	whoami.Name, whoami.Summary, whoami.Examples = "whoami", "Show the signed-in account and server", []string{"coconet whoami"}
	servers := Command{Name: "server", Summary: "Select the default server and list known endpoints", Usage: "<use|list>", Children: []Command{
		{Name: "use", Summary: "Save a default server for new connections", Usage: "<URL|official>", Notes: []string{"Writes only default_server in the local config.json. Does not sign in, contact the server, move data or change connected folders. Use official to restore https://api.coconet.space. Include the full path prefix for subpath deployments."}, Examples: []string{"coconet server use http://dev.example.com:7788", "coconet server use official"}},
		{Name: "list", Summary: "List the default and servers from saved logins and folder connections", Usage: "[--json]", Options: []Option{json}, Notes: []string{"Read-only and offline. Prints the configuration path; never prints credentials. The default only applies outside connected folders."}},
	}}
	agent := Command{Name: "agent", Summary: "Inspect and manage Agent integrations", Usage: "<list|add|remove>", Children: []Command{
		{Name: "list", Summary: "List registered Agent integrations", Usage: "[options]", Options: []Option{json, boolean("verbose", "Show commands, configuration paths and setup errors")}, Examples: []string{"coconet agent list --verbose"}},
		{Name: "add", Summary: "Install or retry the plugin for an Agent", Usage: "<codex|claude|cursor> [options]", Options: []Option{option("command", "CMD", "", "Agent executable; defaults to the chosen Agent name (codex or claude)"), option("config-root", "DIR", "", "Absolute Agent profile directory; defaults to the chosen Agent's normal profile"), json}, Examples: []string{"coconet agent add claude --command tclaude --config-root /path/to/claude-profile"}},
		{Name: "remove", Summary: "Remove one registered Coconet integration", Usage: "<integration-id> [options]", Options: []Option{json}, Notes: []string{"Choose the integration ID from agent list. Native Agent conversation history is preserved."}},
	}}
	inference := Command{Name: "inference", Summary: "Inspect, pause or resume automatic local processing", Usage: "<status|pause|resume>", Notes: []string{"Uses each source Agent's sign-in and configured default model. No separate model or call-budget configuration is needed."}, Children: []Command{
		leaf("status", "Show automatic inference configuration and pause state", ""),
		leaf("pause", "Pause local model processing", ""),
		leaf("resume", "Resume local model processing", ""),
	}, Examples: []string{"coconet inference status"}}
	history := Command{Name: "history", Summary: "Import historical sessions from this connected folder", Usage: "<command> [options]", Options: []Option{
		option("plan", "ID", "", "Required for status/pause/resume/retry/forget; optional for start after a preview"),
		option("agent", "codex|claude|cursor", "", "Required for preview or start without a plan: native history provider"),
		option("sessions", "ID,ID", "", "Optional for start: restrict import to these discovered native Session IDs"),
		option("batch-size", "N", "5", "start: sessions per automatic batch, 1-20"), json,
	}, Notes: []string{"Run in a connected folder. Import is explicit, uses the Agent's default model and consumes its model allowance."}, Children: []Command{
		leaf("preview", "Preview eligible history without importing it", "--agent codex|claude|cursor [--json]"),
		leaf("list", "List existing import plans", "[--json]"),
		leaf("status", "Inspect a selected import plan", "--plan ID [--json]"),
		leaf("start", "Import this folder’s sessions in automatic batches", "--agent codex|claude|cursor | --plan ID [--sessions ID,ID] [--batch-size N] [--json]"),
		leaf("pause", "Pause an import plan", "--plan ID [--json]", "Stops this device’s import work; in-flight calls, publication and already shared tasks may finish."),
		leaf("resume", "Resume a paused import plan", "--plan ID [--json]"),
		leaf("retry", "Retry failed fixed inputs in a plan", "--plan ID [--json]"),
		leaf("forget", "Remove local plan metadata", "--plan ID [--json]", "Only completed plans or unstarted previews can be removed. Published work and native Agent histories are preserved."),
	}, Examples: []string{"coconet history start --agent claude", "coconet history start --plan PLAN_ID --sessions SESSION_A,SESSION_B --batch-size 2"}}
	projection := Command{Name: "projection", Summary: "Inspect processing progress and retry a selected failure", Usage: "<status|retry> [options]", Options: []Option{json,
		option("task", "ID", "", "retry: exact paused model or publication task"),
		option("capture", "ID", "", "retry: exact paused capture event"),
		option("history-batch", "ID", "", "retry: failed history plan"),
	}, Children: []Command{
		leaf("status", "Show capture, model, publication and transfer status", "[--json]"),
		leaf("retry", "Queue one selected failure for retry on this device", "--task ID | --capture ID | --history-batch ID [--json]", "Choose exactly one selector from projection status. Existing processing limits still apply."),
	}, Examples: []string{"coconet projection status", "coconet projection retry --task TASK_ID"}}
	diagnostics := Command{Name: "diagnostics", Summary: "Inspect and manage local processing records", Usage: "<command> [options]", Options: []Option{root, project,
		option("run", "ID", "", "Required for pin/unpin/export: run ID from diagnostics list"),
		option("output", "FILE", "", "Required for export: new file; existing files are never overwritten"),
		boolean("include-content", "export: also include private prompt inputs and model outputs"),
		option("days", "N", "7", "configure: retention of unpinned full archives, 1-365 days"),
		option("max-mib", "N", "256", "configure: archive limit including reserved response space, 32-16384 MiB"),
		option("before", "CURSOR", "", "list: previous page cursor"),
		option("limit", "N", "50", "list: records per page, 1-100"),
	}, Notes: []string{"Retention is shared by the account and deployment. With --root, list/pin/unpin/export also require --project. Output is JSON."}, Children: []Command{
		leaf("status", "Show diagnostic retention and usage", "[options]"),
		leaf("list", "List processing records", "[--limit N] [--before CURSOR] [options]"),
		leaf("configure", "Set archive retention limits", "[--days N] [--max-mib N] [options]", "Omitted settings use the defaults shown above."),
		leaf("pin", "Retain one selected processing archive", "--run ID [options]"),
		leaf("unpin", "Allow normal retention for a selected archive", "--run ID [options]"),
		leaf("export", "Export one processing record", "--run ID --output FILE [--include-content] [options]", "Export excludes private content unless explicitly requested. Review any export before sharing it."),
		leaf("prune", "Apply diagnostic retention rules", "[options]"),
	}, Examples: []string{"coconet diagnostics list --limit 20", "coconet diagnostics export --run RUN_ID --output ./diagnostic.json"}}
	storage := Command{Name: "storage", Summary: "Manage local replicas, limits and offline archives", Usage: "<command> [options]", Options: []Option{root, project,
		option("file", "FILE", "", "Required for export/restore: absolute path to a private replica archive"),
		option("max-mib", "N", "16384", "configure: session-content limit, 16-1048576 MiB; omitted values preserve existing settings"),
		option("exports-mib", "N", "1024", "configure: derived-file limit, 16-1048576 MiB; separate from content blocks"),
	}, Notes: []string{"Limits are shared by the account and deployment. With --root, reindex/export/restore also require --project. Output is JSON."}, Children: []Command{
		leaf("status", "Show local storage usage and limits", "[options]"),
		leaf("configure", "Update selected storage limits", "[--max-mib N] [--exports-mib N] [options]"),
		leaf("prune", "Reclaim unused Coconet content and derived files", "[options]", "Does not delete native Agent conversation history."),
		leaf("reindex", "Rebuild the project's local search index", "[options]"),
		leaf("export", "Archive the project graph and retained local content", "--file /absolute/archive.tar [options]", "Requires all retained content to be present and a new output file. Excludes credentials, unpublished tasks and private model archives."),
		leaf("restore", "Restore a private replica archive", "--file /absolute/archive.tar [options]", "For a connected project, restores content against the current graph without rewinding it. An explicit new --root and --project can create a separate offline recovery store."),
	}, Examples: []string{"coconet storage status", "coconet storage configure --max-mib 8192", "coconet storage export --file /absolute/project-backup.tar"}}
	session := Command{Name: "session", Summary: "Read or continue a fixed synchronized source", Usage: "<read|pull|resume> [options]", Options: append(append([]Option{}, fixed...),
		option("project", "ID", "", "Required: expected project ID; must match this folder's connection"),
		option("agent", "codex|claude|cursor", "", "Required for resume and invalid for read/pull; selects the target Agent"),
		boolean("include-artifact", "read/pull: also materialize the native session artifact"), option("request-id", "ID", "", "resume only: generated if omitted; retry with the printed ID and all original arguments")),
		Notes: []string{"Select --session and --version from graph, Library or search results. Include --node when selecting a node. A synchronized source can be read or continued before a node exists; withdrawn sources need an explicit Library reference. Sources are never silently replaced."}, Children: []Command{
			leaf("read", "Print compact conversation JSONL", "--project ID [--node ID] --session ID --version N [--include-artifact]"),
			leaf("pull", "Return local source paths and metadata as JSON", "--project ID [--node ID] --session ID --version N [--include-artifact]"),
			leaf("resume", "Create a separate native continuation; equivalent to fork", "--project ID [--node ID] --session ID --version N --agent codex|claude|cursor [--request-id ID]"),
		}}
	library := Command{Name: "library", Summary: "List or update collected fixed session sources", Usage: "<list|add|remove> [options]", Options: append(append([]Option{}, fixed...), request,
		option("note", "TEXT", "", "add: optional collection purpose; preserve it when retrying the request"),
		option("limit", "N", "50", "list: nodes per page, 1-100"), option("cursor", "CURSOR", "", "list: next_cursor from the same selection"), json), Children: []Command{
		leaf("list", "List collected sources", "[--limit N] [--cursor CURSOR] [--json]"),
		leaf("add", "Collect a node's fixed session source", "--node ID --session ID --version N [--request-id ID] [--note TEXT] [--json]"),
		leaf("remove", "Remove a source from the collection", "--node ID --session ID --version N [--request-id ID] [--json]"),
	}, Notes: []string{"Run in a connected folder. Members can collect any accessible node. Collection fixes its source independently of the author node and shares state with Dashboard stars. Removal uses the saved Library source, which may differ from the current node."}}
	fork := Command{Name: "fork", Summary: "Create an independent native session from a selected source", Usage: "--agent codex|claude|cursor [--node ID] --session ID --version N [options]", Options: append(append([]Option{}, fixed...), option("agent", "codex|claude|cursor", "", "Required: target Agent; Codex, Claude and Cursor sources support cross-Agent text continuation"), request, json), Notes: []string{"Run in a connected folder. Uses retained local source content, preserves the original history and prints a native continuation command. Same-Agent Fork preserves native history; cross-Agent Fork imports user/assistant text, excluding tools, reasoning and media. Publication can finish later in the background."}, Examples: []string{"coconet fork --agent claude --node NODE_ID --session SESSION_ID --version 3"}}
	account := Command{Name: "account", Summary: "Inspect or replace your account key", Usage: "<key|reset-key> [--server URL]", Options: []Option{server}, Children: []Command{
		leaf("key", "Print the stored account key", "[--server URL]", "Output contains a credential. Keep it private."),
		leaf("reset-key", "Replace the account key", "[--server URL]", "Signs out other devices and browser sessions. If interrupted, repeat the command to complete the same reset."),
	}}
	uninstall := Command{Name: "uninstall", Summary: "Remove Coconet and its Agent integrations", Usage: "[options]", Options: []Option{
		option("root", "DIR", "", "Install root; defaults to $XDG_DATA_HOME/coconet or ~/.local/share/coconet"),
		option("bin-dir", "DIR", "", "Archive-install executable directory; defaults to $XDG_BIN_HOME or ~/.local/bin"),
		boolean("purge", "Also remove Coconet local state, replicas and credentials; native Agent histories are preserved"), json,
	}, Notes: []string{"Without --purge, Coconet state and credentials are retained. Remove the npm launcher afterwards; invoking it again otherwise reinstalls the Runtime."}, Examples: []string{"coconet uninstall", "npm uninstall -g coconet"}}
	return Command{Name: "coconet", Summary: "Coconet · share and continue AI sessions with your team", Usage: "<command> [options]", Children: []Command{
		connect, {Name: "status", Summary: "Show account, project, uploads and Dashboard link", Usage: "[--verbose]", Options: []Option{boolean("verbose", "Show connection and local processing details")}},
		leaf("disconnect", "Disconnect this folder; preserve native history and local session copies", ""),
		login, logout, whoami, servers, agent, inference, history, projection, diagnostics, storage, session, library, fork, account,
		{Name: "health", Summary: "Show machine-readable Runtime and Agent health", Usage: "[--json]", Options: []Option{json}, Notes: []string{"Output is JSON with or without --json."}},
		{Name: "update", Summary: "Update npm and Runtime to the latest stable client", Notes: []string{"Waits for active processing and handoffs before activating the verified Runtime. No automatic installation or sudo. Agent plugins are reconfigured only when their content changes. A running legacy Runtime without safe draining must first finish its work and be stopped explicitly."}},
		leaf("version", "Show the installed version (aliases: --version, -v)", ""), uninstall, init,
		localDAGHelp(),
		{Name: "mcp", Summary: "Run the stdio MCP server for an Agent integration", Usage: "[--agent codex|claude|cursor]", Options: []Option{option("agent", "codex|claude|cursor", "", "Target Agent for native continuation tools; optional")}, Advanced: true},
		{Name: "hook", Summary: "Capture an Agent hook event from stdin", Usage: "--agent codex|claude|cursor [options]", Options: []Option{option("agent", "codex|claude|cursor", "", "Source Agent type"), option("skill-path", "FILE", "", "Installed Codex team-context Skill path; integration-managed")}, Advanced: true, Notes: []string{"Called automatically by the plugin. Ordinary hook execution is silent and fail-open."}},
	}, Notes: []string{"Use `coconet help <command>` or `<command> --help` for parameters. Help does not sign in, connect, run Agents or write local state.", "Run normal project commands in a connected work directory. Private underscore-prefixed worker commands are not a supported user interface."}, Examples: []string{"coconet connect", "coconet status --verbose", "coconet history preview --help"}}
}

func localDAGHelp() Command {
	commands := []Command{}
	for _, entry := range []struct{ name, summary string }{
		{"serve", "Run an explicitly configured development worker"}, {"status", "Inspect worker state"}, {"stop", "Stop the worker"},
		{"observe", "Read a native observation request from JSON"}, {"capture", "Ingest a fixed capture JSON file"}, {"process", "Drain pending tasks using an explicit inference provider"},
		{"sync", "Pull graph updates and flush proposals"}, {"state", "Read accepted project state"}, {"pull", "Materialize the source of a selected node"},
		{"logs", "Read processing runs"}, {"proposals", "Read local proposals"}, {"transfers", "Read recent transport records"}, {"tasks", "Read local tasks"},
		{"retry", "Retry an exact paused task"}, {"inbox", "Inspect captured events"}, {"retry-inbox", "Retry an exact captured event"},
		{"replay", "Replay local graph commits"}, {"index", "Incrementally index project content"}, {"index-rebuild", "Reset and rebuild the local search index"},
	} {
		commands = append(commands, leaf(entry.name, entry.summary, "--root DIR [options]"))
	}
	return Command{Name: "local-dag", Summary: "Low-level worker inspection and isolated development", Usage: "<command> --root DIR [options]", Advanced: true, Children: commands, Options: []Option{
		option("root", "DIR", "", "Required: absolute private store directory"), option("project", "ID", "", "Required except for serve/status/stop/inbox/retry-inbox"),
		option("actor", "ID", "", "Required for capture/process/serve: origin device identity"), option("input", "FILE", "", "Required for capture/observe: fixed JSON input, read-only"),
		option("provider", "codex|claude|cursor", "", "Required for process/serve: inference provider"), option("command", "CMD", "", "Required for process/serve: provider executable"),
		option("profile", "DIR", "", "Required for process/serve: absolute isolated Agent configuration directory"), option("model", "MODEL", "", "Required for process/serve: explicit inference model"),
		option("max-calls", "N", "0", "Required for process/serve, 1-100; per process call or rolling 24 hours for serve"), option("timeout", "DURATION", "2m0s", "Per-provider-call deadline; Go duration such as 120s or 5m"),
		option("task", "ID", "", "retry/retry-inbox: exact task or event ID"), option("node", "ID", "", "Required for pull: current DAG node"), boolean("native", "pull: also materialize the original artifact"),
		option("relay-config", "FILE", "", "Private Relay configuration; defaults to ROOT/relay.json"),
	}, Notes: []string{"Prefer normal managed commands (inference, projection, history, storage). These explicit provider/model/budget options are for development and do not change automatic source-Agent configuration.", "When an existing daemon handles a request, it uses its own configuration. Use a separate store and Agent profile for isolated experiments."}}
}
