package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// User preferences contain no credentials, project bindings or transport flags.
type clientConfig struct {
	DefaultServer string `json:"default_server"`
}

func clientConfigPath(paths runtimePaths) string { return filepath.Join(paths.root, "config.json") }

func defaultServer(paths runtimePaths) (string, error) {
	path := clientConfigPath(paths)
	data, err := readLocalPrivateFile(path, 64<<10, false)
	if errors.Is(err, os.ErrNotExist) {
		return defaultServerURL, nil
	}
	var config clientConfig
	if err == nil {
		err = decodeStrict(data, &config)
	}
	if err == nil {
		var endpoint string
		endpoint, err = normalizeServerURL(config.DefaultServer)
		if err == nil {
			return endpoint, nil
		}
	}
	return "", fmt.Errorf("Could not read the default server in %s: %w. Fix default_server or run `coconet server use <URL|official>`", path, err)
}

func saveDefaultServer(paths runtimePaths, input string) (string, error) {
	if input == "official" {
		input = defaultServerURL
	}
	endpoint, err := normalizeServerURL(input)
	if err != nil {
		return "", err
	}
	if err := atomicWriteJSON(clientConfigPath(paths), clientConfig{DefaultServer: endpoint}, 0600); err != nil {
		return "", err
	}
	return endpoint, nil
}

type serverListItem struct {
	URL              string `json:"url"`
	Default          bool   `json:"default"`
	ConnectedFolders int    `json:"connected_folders"`
}

func runServerCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "use" && args[0] != "list" {
		fmt.Fprintln(stderr, "usage: coconet server use <URL|official> | coconet server list [--json]")
		return 2
	}
	if args[0] == "use" && len(args) != 2 {
		fmt.Fprintln(stderr, "usage: coconet server use <URL|official>")
		return 2
	}
	flags := flag.NewFlagSet("server list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	asJSON := flags.Bool("json", false, "Write machine-readable JSON")
	if args[0] == "list" {
		if flags.Parse(args[1:]) != nil {
			return 2
		}
		if flags.NArg() != 0 {
			fmt.Fprintln(stderr, "usage: coconet server list [--json]")
			return 2
		}
	}
	paths, err := resolveRuntimePaths()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if args[0] == "use" {
		endpoint, err := saveDefaultServer(paths, args[1])
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "Default server: %s\n  New connections use this server. Connected folders keep their own server.\n  Config: %s\n", endpoint, clientConfigPath(paths))
		return 0
	}
	endpoint, err := defaultServer(paths)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	// Read only credential metadata; listing must not access the Keychain or network.
	credentials, err := loadCredentialStateForWrite(paths)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	bindings, err := loadBindingState(paths)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	known := map[string]int{defaultServerURL: 0, endpoint: 0}
	for _, credential := range credentials.Credentials {
		known[credential.ServerURL] = 0
	}
	for _, binding := range bindings.Bindings {
		known[binding.LocalServerURL]++
	}
	items := make([]serverListItem, 0, len(known))
	for url, count := range known {
		items = append(items, serverListItem{URL: url, Default: url == endpoint, ConnectedFolders: count})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].URL < items[j].URL })
	if *asJSON {
		err = json.NewEncoder(stdout).Encode(struct {
			DefaultServer string           `json:"default_server"`
			Config        string           `json:"config"`
			Servers       []serverListItem `json:"servers"`
		}{endpoint, clientConfigPath(paths), items})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	for _, item := range items {
		marker := " "
		if item.Default {
			marker = "*"
		}
		fmt.Fprintf(stdout, "%s %s", marker, item.URL)
		if item.ConnectedFolders > 0 {
			fmt.Fprintf(stdout, " (%d connected folders)", item.ConnectedFolders)
		}
		fmt.Fprintln(stdout)
	}
	fmt.Fprintf(stdout, "\n* Default for new connections\nConfig: %s\n", clientConfigPath(paths))
	return 0
}
