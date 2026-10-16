package codex

import (
	"bufio"
	"strings"
	"testing"
)

func TestImportNotificationBeforeResponseIsRetainedAndMatched(t *testing.T) {
	client := &processClient{scanner: bufio.NewScanner(strings.NewReader(""))}
	client.rememberNotification([]byte(`{"method":"thread/started","params":{}}`))
	client.rememberNotification([]byte(`{"method":"externalAgentConfig/import/completed","params":{"importId":"other"}}`))
	client.rememberNotification([]byte(`{"method":"externalAgentConfig/import/completed","params":{"importId":"mine","itemTypeResults":[]}}`))
	var result struct {
		ImportID string `json:"importId"`
	}
	if err := client.WaitNotification("externalAgentConfig/import/completed", "mine", &result); err != nil || result.ImportID != "mine" || len(client.notifications) != 1 {
		t.Fatal(result, err)
	}
}
