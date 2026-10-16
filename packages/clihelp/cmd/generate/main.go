// Generate the command manuals from the catalog shared with terminal help.
package main

import (
	"os"
	"path/filepath"

	"github.com/xfey/coconet/packages/clihelp"
)

func main() {
	for name, advanced := range map[string]bool{"cli.md": false, "cli-internal.md": true} {
		if err := os.WriteFile(filepath.Join("..", "..", "docs", name), []byte(clihelp.Manual(advanced)), 0644); err != nil {
			panic(err)
		}
	}
}
