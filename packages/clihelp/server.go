package clihelp

func Server() Command {
	config := option("config", "FILE", "", "Required: server.json created by init or a completed deployment restore")
	return Command{Name: "coconet-server", Summary: "Coconet Relay server and deployment tools", Usage: "<command> [options]", Children: []Command{
		{Name: "init", Summary: "Initialize a deployment without starting the server", Usage: "--data-dir DIR --advertised-endpoint URL [options]", Options: []Option{
			option("data-dir", "DIR", "", "Required: persistent deployment directory"),
			option("advertised-endpoint", "URL", "", "Required: canonical client-facing server origin"),
			option("listen", "ADDRESS", "127.0.0.1:7788", "HTTP listen address"),
			option("artifact-store", "filesystem|s3", "filesystem", "Conversation transfer storage driver"),
			option("artifact-path", "DIR", "", "Absolute filesystem artifact directory; defaults to DATA_DIR/artifacts; invalid with s3"),
		}, Notes: []string{"Creates server.json, deployment identity and metadata. Refuses to replace an initialized deployment. Configure login and S3 credentials through the documented environment variables. The advertised endpoint explicitly selects HTTP or HTTPS. HTTP uses Account Key login; OAuth requires HTTPS."}, Examples: []string{"coconet-server init --data-dir /var/lib/coconet --advertised-endpoint https://coconet.example.com"}},
		{Name: "serve", Summary: "Start the Relay HTTP service", Usage: "--config FILE", Options: []Option{config}, Examples: []string{"coconet-server serve --config /var/lib/coconet/server.json"}},
		{Name: "status", Summary: "Inspect the configured deployment", Usage: "--config FILE", Options: []Option{config}, Notes: []string{"This is a local deployment inspection, not a remote health probe."}},
		{Name: "backup", Summary: "Create a complete deployment backup", Usage: "--config FILE --output FILE", Options: []Option{config, option("output", "FILE", "", "Required: new backup file")}, Notes: []string{"Includes identity, metadata and objects still retained by the server. Expired transfer objects require member replicas or client archives; a server backup cannot recreate them. Stop the server first unless using the documented operator-socket backup workflow."}, Examples: []string{"coconet-server backup --config /var/lib/coconet/server.json --output /safe/path/deployment-backup.tar"}},
		{Name: "restore", Summary: "Restore and verify a deployment backup", Usage: "--input FILE (--data-dir DIR | --config FILE)", Options: []Option{
			option("input", "FILE", "", "Required: validated backup file"),
			option("data-dir", "DIR", "", "New empty destination for a complete deployment restore; mutually exclusive with --config"),
			option("config", "FILE", "", "Legacy metadata-only restore target; mutually exclusive with --data-dir"),
		}, Notes: []string{"Use --data-dir for current complete backups. --config only supports legacy metadata-only deployments. Stop the server before restoring; retain the previous directory for rollback."}, Examples: []string{"coconet-server restore --input /safe/path/deployment-backup.tar --data-dir /var/lib/coconet-restored"}},
		{Name: "compact", Summary: "Write a compacted copy of the metadata database", Usage: "--config FILE --output FILE", Options: []Option{config, option("output", "FILE", "", "Required: new compacted database path")}, Notes: []string{"Stop the server first. The original database remains in place; this is not a complete deployment backup."}},
		{Name: "account", Summary: "Local operator account management", Usage: "<create>", Children: []Command{
			{Name: "create", Summary: "Create an account and print its private account key", Usage: "--config FILE --name NAME", Options: []Option{config, option("name", "NAME", "", "Required: valid, nonempty user display name")}, Notes: []string{"For deployments using account-key login. Requires local operator access; stdout contains a credential. Stop the running server before direct database access."}},
		}},
		leaf("version", "Show the server version (aliases: --version, -v)", ""),
	}, Notes: []string{"Help does not open a database, start a listener or emit operator event logs. Use `coconet-server help <command>` for full parameters."}}
}
