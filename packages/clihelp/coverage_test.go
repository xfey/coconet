package clihelp

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Keep public help honest when a command adds, removes or changes a flag.
// These checks inspect real declarations; they never run command handlers.
func TestCatalogCoversOperationalFlags(t *testing.T) {
	routes := map[string]map[string][]string{
		"runtime": {
			"runServerCommand":       {"server list"},
			"parseConnectionOptions": {"connect", "init"}, "runProjectStatus": {"status"},
			"runLoginCommand": {"login", "logout", "whoami"},
			"runAgentAdd":     {"agent add"}, "runAgentList": {"agent list"}, "runAgentRemove": {"agent remove"},
			"runAccount": {"account"}, "runHistoryCommand": {"history"}, "runDiagnostics": {"diagnostics"},
			"runStorage": {"storage"}, "runLocalProjectionCommand": {"projection"}, "runLocalLibraryCommand": {"library"},
			"runNativeForkCommand": {"fork"}, "runSessionCommandWithAdapters": {"session"},
			"runLocalDAG": {"local-dag"}, "runHook": {"hook"}, "runUninstall": {"uninstall"},
		},
		"server": {
			"runServerInit": {"init"}, "parseConfigFlag": {"serve", "status"}, "runServerBackup": {"backup"},
			"runServerRestore": {"restore"}, "runSnapshotCompact": {"compact"}, "runServerAccount": {"account create"},
		},
	}
	for app, mappings := range routes {
		catalog := Runtime()
		if app == "server" {
			catalog = Server()
		}
		files, err := filepath.Glob(filepath.Join("..", "..", "apps", app, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			source, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, decl := range source.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}
				type flagInfo struct {
					boolean bool
					def     ast.Expr
				}
				flags := map[string]flagInfo{}
				ast.Inspect(fn, func(node ast.Node) bool {
					call, ok := node.(*ast.CallExpr)
					if !ok {
						return true
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					receiver, ok := sel.X.(*ast.Ident)
					if !ok || receiver.Name != "flags" {
						return true
					}
					kind := strings.TrimSuffix(sel.Sel.Name, "Var")
					switch kind {
					case "String", "Bool", "Int", "Int64", "Duration":
					default:
						return true
					}
					index := 0
					if strings.HasSuffix(sel.Sel.Name, "Var") {
						index++
					}
					literal, ok := call.Args[index].(*ast.BasicLit)
					if !ok {
						t.Fatalf("%s: computed flag name needs explicit coverage", fn.Name.Name)
					}
					name, _ := strconv.Unquote(literal.Value)
					flags[name] = flagInfo{kind == "Bool", call.Args[index+1]}
					return true
				})
				if len(flags) == 0 {
					continue
				}
				topics, found := mappings[fn.Name.Name]
				if !found {
					t.Fatalf("%s/%s: flag parser missing help coverage mapping", app, fn.Name.Name)
				}
				seen[fn.Name.Name] = true
				for _, topic := range topics {
					c := catalog
					for _, word := range strings.Fields(topic) {
						var ok bool
						c, ok = c.child(word)
						if !ok {
							t.Fatalf("missing topic %s", topic)
						}
					}
					if len(c.Options) != len(flags) {
						t.Errorf("%s %s: %d documented flags, %d declared", app, topic, len(c.Options), len(flags))
					}
					for _, option := range c.Options {
						actual, ok := flags[option.Name]
						if !ok || actual.boolean != (option.Value == "") || option.Description == "" {
							t.Errorf("%s %s: --%s is absent, has the wrong arity or lacks a description", app, topic, option.Name)
							continue
						}
						var expected string
						switch value := actual.def.(type) {
						case *ast.BasicLit:
							expected = value.Value
							if value.Kind == token.STRING {
								expected, _ = strconv.Unquote(value.Value)
							}
						case *ast.Ident:
							if value.Name != "false" && value.Name != "true" {
								continue // Environment-dependent defaults are described in prose.
							}
							expected = value.Name
						default:
							continue
						}
						if option.Default != expected {
							t.Errorf("%s %s --%s: default %q differs from parser %q", app, topic, option.Name, option.Default, expected)
						}
					}
				}
			}
		}
		for name := range mappings {
			if !seen[name] {
				t.Errorf("%s/%s: stale help coverage mapping", app, name)
			}
		}
	}
}
