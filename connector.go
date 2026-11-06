package main

// flowhub as a flow human connector (contract v1).
//
//	flow ─► connector/flowhub/run send ─► POST /api/connector/ask  (records a "ping")
//	hub UI reply/seen ─► FLOW_CONNECTOR_TOKEN=<secret> flow human reply|seen <id>
//
// The hub reads the connector secret itself, with the same lookup as
// flow's SecretStore.Get. The secret is never logged, never sent to the
// browser and never put in an error string.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

const connectorName = "flowhub"

// keychainAccount mirrors flow's connector.KeychainAccount: "flow-" + the
// first 12 hex chars of sha256 of the absolute, symlink-resolved, cleaned
// flow root.
func keychainAccount(flowRoot string) string {
	p := flowRoot
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	sum := sha256.Sum256([]byte(filepath.Clean(p)))
	return "flow-" + hex.EncodeToString(sum[:])[:12]
}

func securityKeychainGet(service, account string) (string, error) {
	if runtime.GOOS != "darwin" {
		return "", errors.New("no keychain")
	}
	var out bytes.Buffer
	cmd := exec.Command("security", "find-generic-password", "-a", account, "-s", service, "-w")
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", errors.New("no keychain item")
	}
	return strings.TrimRight(out.String(), "\n"), nil
}

// connectorSecret looks up the flowhub connector secret for one flow home.
// Read lazily on every write, so installing/removing the connector takes
// effect without restarting the hub.
type connectorSecret struct {
	root        string
	keychainGet func(service, account string) (string, error)
}

func newConnectorSecret(root string) *connectorSecret {
	return &connectorSecret{root: root, keychainGet: securityKeychainGet}
}

// Get returns the secret, or "" when the connector isn't installed.
func (c *connectorSecret) Get() string {
	if c == nil {
		return ""
	}
	if c.keychainGet != nil {
		if v, err := c.keychainGet("flow-connector-"+connectorName, keychainAccount(c.root)); err == nil && v != "" {
			return v
		}
	}
	b, err := os.ReadFile(filepath.Join(c.root, "secrets", connectorName))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// ---- pings: asks flow told us about, surfaced to the UI ----

type pingSet struct {
	mu  sync.Mutex
	ids []string
	max int
}

func newPingSet(max int) *pingSet { return &pingSet{max: max} }

func (p *pingSet) add(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, x := range p.ids {
		if x == id {
			return
		}
	}
	p.ids = append(p.ids, id)
	if len(p.ids) > p.max {
		p.ids = p.ids[len(p.ids)-p.max:]
	}
}

func (p *pingSet) list() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string{}, p.ids...)
}

// connectorAskHandler serves POST /api/connector/ask — called only by
// connector/flowhub/run (curl). Browsers are refused: any Origin header is
// rejected, the custom X-Flow-Connector header is required (a cross-site
// form can't set it without a preflight we never answer), and only
// loopback peers are accepted.
func connectorAskHandler(p *pingSet) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Origin") != "" || r.Header.Get("X-Flow-Connector") != "1" || !loopbackPeer(r) {
			http.Error(w, "connector endpoint", http.StatusForbidden)
			return
		}
		var ask struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&ask); err != nil || ask.ID == "" {
			http.Error(w, "bad ask", http.StatusBadRequest)
			return
		}
		if ask.Kind != "test" {
			p.add(ask.ID)
		}
		writeJSON(w, map[string]any{"ok": true})
	}
}

func loopbackPeer(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ---- inbox writes: connector path with a plain-message fallback ----

var errNotConnector = errors.New("flow: not a connector")

type humanWriter interface {
	HumanSeen(tok, id string) error
	HumanReply(tok, id, text string) error
	Reply(task, text string) error
}

const seenHint = "install the flowhub connector to mark mail seen"

// threadSeen marks ids seen through the connector. Without a (valid)
// secret it is a no-op with a hint — flow only lets a connector mark
// human mail seen.
func threadSeen(w humanWriter, tok string, ids []string) (seen []string, hint string, err error) {
	if tok == "" {
		return nil, seenHint, nil
	}
	for _, id := range ids {
		if err := w.HumanSeen(tok, id); err != nil {
			if errors.Is(err, errNotConnector) {
				return seen, seenHint, nil
			}
			return seen, "", err
		}
		seen = append(seen, id)
	}
	return seen, "", nil
}

// threadReply answers the thread's newest unanswered ask via `flow human
// reply` (marks it answered, reply_to=<ask>). With no secret, no open ask,
// or a secret flow doesn't accept, it falls back to a plain message.
// Returns which path was used: "connector" | "message".
func threadReply(w humanWriter, tok string, th *InboxThread, task, text string) (string, error) {
	if tok != "" && th != nil && th.AskID != "" {
		err := w.HumanReply(tok, th.AskID, text)
		if err == nil {
			return "connector", nil
		}
		if !errors.Is(err, errNotConnector) {
			return "", err
		}
	}
	if err := w.Reply(task, text); err != nil {
		return "", err
	}
	return "message", nil
}
