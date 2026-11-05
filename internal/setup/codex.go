package setup

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// TrustCodexHooks marks agent-probe's own hooks trusted in Codex — Codex
// runs a hook only once its hash is trusted (/hooks in the TUI). It asks
// `codex app-server` (JSON-RPC on stdio, what /hooks itself uses) for the
// hooks and their hashes, and writes the trust of those whose command
// carries agent-probe's marker — never anyone else's. Returns how many it
// trusted.
func TrustCodexHooks(codex string, timeout time.Duration) (int, error) {
	if codex == "" {
		p, err := exec.LookPath("codex")
		if err != nil {
			return 0, errors.New("codex is not on PATH")
		}
		codex = p
	}
	cmd := exec.Command(codex, "app-server")
	cmd.Stderr = nil
	in, err := cmd.StdinPipe()
	if err != nil {
		return 0, err
	}
	outp, err := cmd.StdoutPipe()
	if err != nil {
		return 0, err
	}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	defer func() {
		in.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	type reply struct {
		ID     int             `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	replies := make(chan reply, 16)
	go func() {
		sc := bufio.NewScanner(outp)
		sc.Buffer(make([]byte, 1<<20), 16<<20)
		for sc.Scan() {
			var r reply
			if json.Unmarshal(sc.Bytes(), &r) == nil && r.ID > 0 {
				replies <- r
			}
		}
		close(replies)
	}()
	deadline := time.After(timeout)
	send := func(w io.Writer, v any) error {
		raw, _ := json.Marshal(v)
		_, err := w.Write(append(raw, '\n'))
		return err
	}
	call := func(id int, method string, params any) (json.RawMessage, error) {
		if err := send(in, map[string]any{"id": id, "method": method, "params": params}); err != nil {
			return nil, err
		}
		for {
			select {
			case r, ok := <-replies:
				if !ok {
					return nil, errors.New("codex app-server exited")
				}
				if r.ID != id {
					continue
				}
				if r.Error != nil {
					return nil, fmt.Errorf("%s: %s", method, r.Error.Message)
				}
				return r.Result, nil
			case <-deadline:
				return nil, fmt.Errorf("%s: no answer from codex app-server", method)
			}
		}
	}
	if _, err := call(1, "initialize", map[string]any{"clientInfo": map[string]string{"name": "agent-probe", "version": "1"}}); err != nil {
		return 0, err
	}
	if err := send(in, map[string]any{"method": "initialized"}); err != nil {
		return 0, err
	}
	home, _ := os.UserHomeDir()
	res, err := call(2, "hooks/list", map[string]any{"cwds": []string{home}})
	if err != nil {
		return 0, err
	}
	var list struct {
		Data []struct {
			Hooks []struct {
				Key         string `json:"key"`
				Command     string `json:"command"`
				CurrentHash string `json:"currentHash"`
				TrustStatus string `json:"trustStatus"`
			} `json:"hooks"`
		} `json:"data"`
	}
	if err := json.Unmarshal(res, &list); err != nil {
		return 0, err
	}
	var edits []map[string]any
	for _, d := range list.Data {
		for _, h := range d.Hooks {
			if !strings.Contains(h.Command, Marker) || h.CurrentHash == "" || h.TrustStatus == "trusted" {
				continue
			}
			edits = append(edits, map[string]any{"keyPath": `hooks.state."` + h.Key + `"`,
				"value": map[string]string{"trusted_hash": h.CurrentHash}, "mergeStrategy": "upsert"})
		}
	}
	if len(edits) == 0 {
		return 0, nil
	}
	if _, err := call(3, "config/batchWrite", map[string]any{"edits": edits}); err != nil {
		return 0, err
	}
	return len(edits), nil
}
