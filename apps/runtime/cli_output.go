package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/xfey/coconet/packages/agentplugin"
	"github.com/xfey/coconet/packages/clihelp"
	"github.com/xfey/coconet/packages/cliui"
)

type cliField struct {
	Label string
	Value string
}

func writeCLISuccess(output io.Writer, message string) {
	cliui.Success(output, message)
}

func writeCLIFields(output io.Writer, fields ...cliField) {
	width := 0
	for _, field := range fields {
		if field.Value != "" && len(field.Label) > width {
			width = len(field.Label)
		}
	}
	for _, field := range fields {
		if field.Value == "" {
			continue
		}
		fmt.Fprintf(output, "%-*s  %s\n", width, field.Label, field.Value)
	}
}

func isHostedServer(serverURL string) bool {
	return strings.TrimRight(serverURL, "/") == strings.TrimRight(defaultServerURL, "/")
}

func displayAgentType(host agentplugin.Host) string {
	switch host {
	case agentplugin.HostCodex:
		return "Codex"
	case agentplugin.HostCursor:
		return "Cursor"
	case agentplugin.HostClaude:
		return "Claude"
	default:
		return string(host)
	}
}

func displayAgentStatus(status agentIntegrationStatus) string {
	switch status {
	case agentIntegrationConfigured:
		return "Plugin installed"
	case agentIntegrationPending:
		return "Setup pending"
	case agentIntegrationNotDetected:
		return "Command not found"
	case agentIntegrationFailed:
		return "Setup failed"
	default:
		return string(status)
	}
}

func writeCLIHelp(output io.Writer) {
	root := clihelp.Runtime()
	clihelp.Write(output, root, root.Name, nil)
}
