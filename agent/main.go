package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func runAgent(root, runDir string) error {
	tokenData, err := os.ReadFile(filepath.Join(root, "agent.token"))
	if err != nil {
		return errors.New("management token unavailable")
	}
	token := strings.TrimSpace(string(tokenData))
	cfgData, err := os.ReadFile(filepath.Join(root, "config.json"))
	if err != nil {
		return errors.New("core configuration unavailable")
	}
	var cfg struct {
		Secret         string   `json:"secret"`
		Authentication []string `json:"authentication"`
	}
	if json.Unmarshal(cfgData, &cfg) != nil || cfg.Secret == "" {
		return errors.New("core controller credentials unavailable")
	}
	core := &CoreClient{BaseURL: "http://127.0.0.1:9090", Token: cfg.Secret}
	service := &ScriptService{Root: root, RunDir: runDir}
	downloadTransport := http.DefaultTransport.(*http.Transport).Clone()
	downloadTransport.Proxy = nil
	if len(cfg.Authentication) > 0 {
		user, password, ok := strings.Cut(cfg.Authentication[0], ":")
		if !ok {
			return errors.New("proxy credential format invalid")
		}
		proxy := &url.URL{Scheme: "http", Host: "127.0.0.1:7890", User: url.UserPassword(user, password)}
		downloadTransport.Proxy = func(r *http.Request) (*url.URL, error) {
			ip := net.ParseIP(r.URL.Hostname())
			if ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()) {
				return nil, nil
			}
			return proxy, nil
		}
	}
	download := &http.Client{Transport: downloadTransport, Timeout: 45 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) >= 5 || ValidateSubscriptionURL(r.URL.String()) != nil {
			return errors.New("subscription redirect rejected")
		}
		return nil
	}}
	validate := func(ctx context.Context, path string) error {
		child, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		command := exec.CommandContext(child, "/tmp/mihomo", "-t", "-d", runDir, "-f", path)
		command.Env = append(os.Environ(), "SAFE_PATHS="+root, "GOMEMLIMIT=96MiB", "GOGC=50")
		output, e := command.CombinedOutput()
		if e != nil {
			_ = os.WriteFile(filepath.Join(runDir, "validate-last-error.log"), output, 0600)
			return errors.New("candidate configuration rejected")
		}
		return nil
	}
	server, err := NewServer(Options{Root: root, RunDir: runDir, CorePath: "/tmp/mihomo", Token: token, Core: core, Service: service, Validate: validate, Download: download, ArchiveSHA256: coreArchiveSHA, BinarySHA256: coreBinarySHA, ArchiveBytes: coreArchiveSize})
	if err != nil {
		return err
	}
	identity, err := EnsureCertificate(root, "192.168.31.1")
	if err != nil {
		return err
	}
	pairing, _ := json.Marshal(map[string]any{"schema": 1, "routerUrl": "https://192.168.31.1:9443", "token": token, "certificateSha256": identity.Fingerprint})
	if err = atomicWrite(filepath.Join(root, "pairing.json"), pairing); err != nil {
		return err
	}
	curlConfig := localHealthCurlConfig(identity, token)
	if err = atomicWrite(filepath.Join(root, "agent-curl.conf"), []byte(curlConfig)); err != nil {
		return err
	}
	https := &http.Server{Addr: "192.168.31.1:9443", Handler: server, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 90 * time.Second, WriteTimeout: 180 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 * 1024, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	return https.ListenAndServeTLS(identity.CertPath, identity.KeyPath)
}

func main() {
	root := flag.String("root", "/data/codex-router", "private persistent directory")
	runDir := flag.String("run", "/tmp/codex-mihomo", "volatile runtime directory")
	restore := flag.Bool("restore-selections", false, "restore persisted selector state after core start")
	patchCore := flag.Bool("apply-core-patch", false, "verify official base and restore pinned core in RAM")
	relays := flag.Bool("write-relay-bypass", false, "resolve configured upstream address/port pairs for LAN proxy coexistence")
	flag.Parse()
	if *relays {
		if err := WriteRelayBypass(context.Background(), *root, *runDir, nil); err != nil {
			log.Fatal("upstream relay planning failed")
		}
		return
	}
	if *patchCore {
		if err := ApplyCorePatch("/tmp/mihomo.base", filepath.Join(*root, "core-update.bsdiff"), "/tmp/mihomo", coreBaseBinarySHA, corePatchSHA, coreBinarySHA); err != nil {
			log.Fatal(err)
		}
		return
	}
	if *restore {
		cfg, state, err := (&Store{Root: *root}).Snapshot()
		if err != nil {
			log.Fatal("persisted selections unavailable")
		}
		secret, _ := cfg["secret"].(string)
		server := &Server{options: Options{Core: &CoreClient{BaseURL: "http://127.0.0.1:9090", Token: secret}}}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err = server.restoreSelections(ctx, state); err != nil {
			log.Fatal("selector restoration incomplete")
		}
		return
	}
	if err := runAgent(*root, *runDir); err != nil {
		log.Fatal(err)
	}
}
