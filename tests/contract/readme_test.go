package contract_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Documentation follows architectural entry points. Leaf schema, fixture and
// dependency directories do not need generated file-list READMEs.
func TestSourceEntryPointsHaveDocumentation(t *testing.T) {
	root := repositoryRoot(t)
	for _, directory := range []string{
		".", "adapters", "apps/runtime", "apps/server", "apps/dashboard", "apps/release",
		"packages", "packages/workgraph", "packages/graphstore", "packages/replication",
		"packages/contracts", "plugins", "prompts", "tests", "installers/npm", "deploy",
	} {
		content, err := os.ReadFile(filepath.Join(root, directory, "README.md"))
		if err != nil || strings.TrimSpace(string(content)) == "" {
			t.Errorf("missing module documentation for %s: %v", directory, err)
		}
	}
}
