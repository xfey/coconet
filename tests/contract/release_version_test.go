package contract_test

import (
	"regexp"
	"testing"
)

type internalVersionPolicy struct {
	SchemaVersion int    `json:"schema_version"`
	Channel       string `json:"channel"`
	ClientVersion string `json:"client_version"`
	ServerVersion string `json:"server_version"`
}

type setupPackageManifest struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Private bool   `json:"private"`
}

type releaseChecksumManifest struct {
	SchemaVersion int               `json:"schema_version"`
	Version       string            `json:"version"`
	Repository    string            `json:"repository"`
	ReleaseTag    string            `json:"release_tag"`
	Archives      map[string]string `json:"archives"`
}

func TestInternalCandidateVersionsStayUniqueAndAligned(t *testing.T) {
	root := repositoryRoot(t)
	policy := decodeJSON[internalVersionPolicy](t, root+"/apps/release/internal-versions.json")
	internalVersion := regexp.MustCompile(`^\d+\.\d+\.\d+-internal\.[1-9]\d*$`)
	if policy.SchemaVersion != 1 || policy.Channel != "internal" || !internalVersion.MatchString(policy.ClientVersion) || !internalVersion.MatchString(policy.ServerVersion) {
		t.Fatalf("invalid internal release policy: %#v", policy)
	}

	setup := decodeJSON[setupPackageManifest](t, root+"/installers/npm/package.json")
	codex := decodeJSON[pluginManifest](t, root+"/plugins/codex/coconet/.codex-plugin/plugin.json")
	claude := decodeJSON[pluginManifest](t, root+"/plugins/claude/coconet/.claude-plugin/plugin.json")
	checksums := decodeJSON[releaseChecksumManifest](t, root+"/installers/npm/release-manifest.json")
	if setup.Name != "coconet" || setup.Version != policy.ClientVersion || codex.Version != claude.Version || checksums.Version != policy.ClientVersion {
		t.Fatalf("internal client versions diverged: policy=%#v setup=%#v codex=%#v claude=%#v checksums=%#v", policy, setup, codex, claude, checksums)
	}
	if !setup.Private {
		t.Fatal("internal npm setup package must remain private until public release is authorized")
	}
	if checksums.SchemaVersion != 2 || checksums.Repository != "xfey/coconet" || checksums.ReleaseTag != "v"+policy.ClientVersion || len(checksums.Archives) != 0 {
		t.Fatalf("internal npm setup must not expose a default remote release: %#v", checksums)
	}
}
