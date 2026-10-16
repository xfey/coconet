package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/xfey/coconet/packages/localdag"
	"github.com/xfey/coconet/packages/replication"
	"github.com/xfey/coconet/packages/workgraph"
)

type localDAGRelayConfig struct {
	SchemaVersion int      `json:"schema_version"`
	Endpoint      string   `json:"endpoint"`
	AccountID     string   `json:"account_id"`
	DeviceID      string   `json:"device_id"`
	TokenFile     string   `json:"token_file"`
	Projects      []string `json:"projects"`
}

func readLocalPrivateFile(path string, limit int64, secret bool) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("configuration path must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit || secret && info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("configuration must be a bounded regular file; credentials require mode 0600")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if int64(len(data)) > limit {
		return nil, errors.New("configuration exceeds size limit")
	}
	return data, err
}

// Explicit Relay configuration is confined to this private DAG root. Normal
// installation/binding will supply the same values from its credential store.
func configureLocalDAGRelay(root, path, project string, engine *localdag.Engine) error {
	if path == "" {
		if managed, err := configureConnectedLocalDAG(root, engine); managed {
			return err
		}
		path = filepath.Join(root, "relay.json")
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			return engine.Store.BindMode("offline")
		}
	}
	data, err := readLocalPrivateFile(path, 64<<10, false)
	if err != nil {
		return err
	}
	var config localDAGRelayConfig
	if err := decodeStrict(data, &config); err != nil {
		return err
	}
	id := regexp.MustCompile(`^[A-Za-z0-9_-]{1,96}$`)
	endpoint, err := url.Parse(config.Endpoint)
	if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || config.SchemaVersion != 1 || !id.MatchString(config.AccountID) || !id.MatchString(config.DeviceID) || len(config.Projects) == 0 || len(config.Projects) > 100 {
		return errors.New("invalid Relay configuration")
	}
	ip := net.ParseIP(endpoint.Hostname())
	local := endpoint.Hostname() == "localhost" || ip != nil && ip.IsLoopback()
	if endpoint.Scheme != "https" && !(endpoint.Scheme == "http" && local) {
		return errors.New("Relay endpoint requires HTTPS except loopback")
	}
	allowed := project == ""
	for _, p := range config.Projects {
		if !id.MatchString(p) {
			return errors.New("invalid Relay project identity")
		}
		allowed = allowed || p == project
	}
	if !allowed {
		return errors.New("project is not included in this Relay configuration")
	}
	secret, err := readLocalPrivateFile(config.TokenFile, 8192, true)
	if err != nil {
		return err
	}
	token := strings.TrimSpace(string(secret))
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return errors.New("invalid Relay credential")
	}
	httpClient, err := coconetHTTPClient(60 * time.Second)
	if err != nil {
		return err
	}
	remote := &replication.Client{Store: engine.Store, Remote: replication.HTTP{BaseURL: strings.TrimRight(config.Endpoint, "/"), Token: token, Client: httpClient}, Account: config.AccountID, Device: config.DeviceID}
	if engine.Actor != "" && engine.Actor != remote.Writer() {
		return errors.New("--actor differs from authenticated Relay writer")
	}
	identity := workgraph.Digest([]string{strings.TrimRight(config.Endpoint, "/"), config.AccountID, config.DeviceID})
	if err := engine.Store.BindMode("relay/" + identity); err != nil {
		return err
	}
	for _, p := range config.Projects {
		if err := engine.Store.WatchProject(p); err != nil {
			return err
		}
	}
	engine.Actor, engine.Publisher = remote.Writer(), remote
	return nil
}

func syncLocalDAG(ctx context.Context, engine *localdag.Engine, project string) error {
	return syncLocalDAGMode(ctx, engine, project, false)
}
func syncLocalDAGBackground(ctx context.Context, engine *localdag.Engine, project string) error {
	return syncLocalDAGMode(ctx, engine, project, true)
}
func syncLocalDAGMode(ctx context.Context, engine *localdag.Engine, project string, deferred bool) error {
	remote, ok := engine.Publisher.(interface {
		Sync(context.Context, string) error
	})
	if !ok {
		return errors.New("this store has no Relay configuration")
	}
	if err := remote.Sync(ctx, project); err != nil {
		return err
	}
	if err := engine.Reconcile(project); err != nil {
		return err
	}
	if deferred {
		if publisher, ok := remote.(interface {
			PublishCheckpointDeferred(context.Context, string) error
		}); ok {
			return publisher.PublishCheckpointDeferred(ctx, project)
		}
	}
	if publisher, ok := remote.(interface {
		PublishCheckpoint(context.Context, string) error
	}); ok {
		return publisher.PublishCheckpoint(ctx, project)
	}
	return nil
}
