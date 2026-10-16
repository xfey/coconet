package integration

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/xfey/coconet/packages/contracts"
)

// Existing sync fixtures start with operator-issued identities. Browser and CLI
// authorization are exercised separately; fixture setup never enables signup.
var fixtureAccounts = struct {
	sync.Mutex
	keys     map[string][]string
	assigned map[string]string
}{keys: map[string][]string{}, assigned: map[string]string{}}

func seedFixtureAccounts(t *testing.T, binary, configPath string) {
	t.Helper()
	var identity struct {
		DeploymentID string `json:"deployment_id"`
	}
	decodeFile(t, filepath.Join(filepath.Dir(configPath), "deployment.json"), &identity)
	var keys []string
	for _, name := range []string{"Fixture Owner", "Fixture Member", "Fixture Reader", "Fixture Other"} {
		command := exec.Command(binary, "account", "create", "--config", configPath, "--name", name)
		content, err := command.Output()
		if err != nil {
			t.Fatal("could not create isolated fixture account", err)
		}
		keys = append(keys, strings.TrimSpace(string(content)))
	}
	fixtureAccounts.Lock()
	fixtureAccounts.keys[identity.DeploymentID] = keys
	fixtureAccounts.Unlock()
	t.Cleanup(func() {
		fixtureAccounts.Lock()
		delete(fixtureAccounts.keys, identity.DeploymentID)
		fixtureAccounts.Unlock()
	})
}

func seedFixtureLogin(t *testing.T, home, serverURL string) {
	t.Helper()
	path := filepath.Join(home, "credentials.json")
	var state struct {
		SchemaVersion int              `json:"schema_version"`
		Credentials   []map[string]any `json:"credentials"`
	}
	content, err := os.ReadFile(path)
	if err == nil {
		if json.Unmarshal(content, &state) != nil {
			t.Fatal("invalid fixture credential state")
		}
		for _, credential := range state.Credentials {
			if credential["server_url"] == serverURL {
				return
			}
		}
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	response, err := http.Get(serverURL + "/v1/deployment?challenge=fixture_account_setup_1234")
	if err != nil {
		t.Fatal("fixture Deployment is unavailable", err)
	}
	var deployment contracts.DeploymentInfo
	err = json.NewDecoder(response.Body).Decode(&deployment)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, credential := range state.Credentials {
		if credential["deployment_id"] == deployment.DeploymentID {
			return
		}
	}
	fixtureAccounts.Lock()
	assignment := deployment.DeploymentID + "\x00" + home
	key := fixtureAccounts.assigned[assignment]
	if key == "" {
		keys := fixtureAccounts.keys[deployment.DeploymentID]
		if len(keys) > 0 {
			key = keys[0]
			fixtureAccounts.keys[deployment.DeploymentID] = keys[1:]
			fixtureAccounts.assigned[assignment] = key
		}
	}
	fixtureAccounts.Unlock()
	if key == "" {
		t.Fatal("no isolated fixture account available")
	}
	request, _ := http.NewRequest(http.MethodGet, serverURL+"/v1/account", nil)
	request.Header.Set("Authorization", "Bearer "+key)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var account contracts.Account
	err = json.NewDecoder(response.Body).Decode(&account)
	response.Body.Close()
	if err != nil || account.AccountID == "" {
		t.Fatal("fixture account is unavailable")
	}
	state.SchemaVersion = contracts.LocalCredentialSchemaVersion
	state.Credentials = append(state.Credentials, map[string]any{"schema_version": contracts.LocalCredentialSchemaVersion, "deployment_id": deployment.DeploymentID, "server_url": serverURL, "server_public_key_sha256": deployment.PublicKeySHA256, "account": account, "access_token": key, "token_storage": "file"})
	content, err = json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}
