package codexobserve

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"path"
	"regexp"
	"strings"
	"sync"
)

// PeerIdentity identifies the local IPC router, never an individual chat agent.
// No executable path or user identifier is exposed to the observer.
type PeerIdentity struct {
	PID              int32
	ProcessStartTime int64
	ExecutableName   string
	SourceID         string
}

var errPeer = errors.New("native Codex IPC peer is unavailable")
var codexMSIX = regexp.MustCompile(`^openai\.codex_[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+_(x64|arm64)__2p2nqsd0c76g0$`)

func peerIdentity(pid int32, started int64, executable string) PeerIdentity {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%d:%d:%s", pid, started, executable)))
	return PeerIdentity{PID: pid, ProcessStartTime: started, ExecutableName: executable, SourceID: "codex-ipc-" + hex.EncodeToString(digest[:16])}
}

type verifiedConn struct {
	net.Conn
	mu      sync.Mutex
	peer    PeerIdentity
	verify  func(context.Context) error
	release func()
	closed  bool
}

func (c *verifiedConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	err := c.Conn.Close()
	if c.release != nil {
		c.release()
	}
	return err
}

// VerifyPeer checks the same authenticated connection and original process
// identity. PID reuse, closed connections, and arbitrary net.Conn values fail.
func VerifyPeer(ctx context.Context, conn net.Conn, expected PeerIdentity) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c, ok := conn.(*verifiedConn)
	if !ok {
		return errPeer
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.peer != expected || c.verify == nil {
		return errPeer
	}
	if err := c.verify(ctx); err != nil {
		return errPeer
	}
	return ctx.Err()
}

func windowsImageName(executable, programFiles, programFilesX86, localAppData string) (string, bool) {
	normalize := func(value string) string {
		value = strings.ReplaceAll(value, "\\", "/")
		if len(value) < 3 || value[1] != ':' || value[2] != '/' {
			return ""
		}
		if path.Clean(value) != value {
			return ""
		}
		return strings.ToLower(value)
	}
	image := normalize(executable)
	if image == "" {
		return "", false
	}
	for _, value := range []string{programFiles, programFilesX86} {
		root := normalize(strings.TrimRight(value, "/\\"))
		if root == "" {
			continue
		}
		prefix := root + "/windowsapps/"
		if strings.HasPrefix(image, prefix) {
			parts := strings.Split(strings.TrimPrefix(image, prefix), "/")
			if len(parts) == 3 && codexMSIX.MatchString(parts[0]) && parts[1] == "app" && parts[2] == "chatgpt.exe" {
				return "ChatGPT.exe", true
			}
		}
		if image == root+"/microsoft vs code/code.exe" {
			return "Code.exe", true
		}
	}
	local := normalize(strings.TrimRight(localAppData, "/\\"))
	if local != "" && image == local+"/programs/microsoft vs code/code.exe" {
		return "Code.exe", true
	}
	return "", false
}

func darwinImageName(executable string) (string, bool) {
	// These are installed vendor bundle layouts, not basename matches.
	for _, root := range []string{"/Applications/ChatGPT.app", "/Applications/Visual Studio Code.app"} {
		name := "ChatGPT"
		if strings.HasSuffix(root, "Visual Studio Code.app") {
			name = "Code"
		}
		if executable == root+"/Contents/MacOS/"+name {
			return name, true
		}
		if name == "Code" {
			for _, helper := range []string{"Code Helper", "Code Helper (Plugin)", "Code Helper (Renderer)", "Code Helper (GPU)"} {
				if executable == root+"/Contents/Frameworks/"+helper+".app/Contents/MacOS/"+helper {
					return helper, true
				}
			}
		}
	}
	return "", false
}
