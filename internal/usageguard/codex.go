package usageguard

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"
)

// CodexReader uses Codex's local app-server JSON-RPC API. It intentionally
// queries the installed CLI instead of an undocumented remote endpoint, so it
// uses the same authenticated account and credential home as the agent.
type CodexReader struct {
	// Command is primarily a test seam. When nil, "codex app-server" is used.
	Command func(context.Context, []string) *exec.Cmd
}

func (r CodexReader) Fetch(ctx context.Context, env []string) (Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	command := r.Command
	if command == nil {
		command = func(ctx context.Context, env []string) *exec.Cmd {
			cmd := exec.CommandContext(ctx, "codex", "app-server")
			if len(env) > 0 {
				cmd.Env = env
			}
			return cmd
		}
	}
	cmd := command(ctx, env)
	if cmd.Env == nil && len(env) > 0 {
		cmd.Env = env
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open Codex app-server stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open Codex app-server stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start Codex app-server: %w", err)
	}
	defer func() {
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	enc := json.NewEncoder(stdin)
	if err := enc.Encode(rpcRequest{JSONRPC: "2.0", ID: 1, Method: "initialize", Params: map[string]any{"clientInfo": map[string]string{"name": "orq-lite", "version": "1"}}}); err != nil {
		return nil, fmt.Errorf("initialize Codex app-server: %w", err)
	}
	scanner := bufio.NewScanner(stdout)
	// Responses are small; this keeps a malformed stream from consuming memory.
	scanner.Buffer(make([]byte, 1024), 1<<20)
	if _, err := awaitRPC(scanner, 1); err != nil {
		return nil, err
	}
	if err := enc.Encode(rpcRequest{JSONRPC: "2.0", Method: "initialized"}); err != nil {
		return nil, fmt.Errorf("confirm Codex app-server initialization: %w", err)
	}
	if err := enc.Encode(rpcRequest{JSONRPC: "2.0", ID: 2, Method: "account/rateLimits/read"}); err != nil {
		return nil, fmt.Errorf("request Codex rate limits: %w", err)
	}
	result, err := awaitRPC(scanner, 2)
	if err != nil {
		return nil, err
	}
	return parseCodexRateLimits(result)
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func awaitRPC(scanner *bufio.Scanner, id int) (json.RawMessage, error) {
	for scanner.Scan() {
		var response rpcResponse
		if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
			continue // notification or unrelated app-server message
		}
		if response.ID != id {
			continue
		}
		if response.Error != nil {
			return nil, fmt.Errorf("Codex app-server request failed: %s", response.Error.Message)
		}
		return response.Result, nil
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read Codex app-server response: %w", err)
	}
	return nil, fmt.Errorf("Codex app-server closed before response %d", id)
}

func parseCodexRateLimits(raw json.RawMessage) (Snapshot, error) {
	var response struct {
		RateLimits *struct {
			Primary *struct {
				UsedPercent        float64 `json:"usedPercent"`
				WindowDurationMins int     `json:"windowDurationMins"`
				ResetsAt           int64   `json:"resetsAt"`
			} `json:"primary"`
			Secondary *struct {
				UsedPercent        float64 `json:"usedPercent"`
				WindowDurationMins int     `json:"windowDurationMins"`
				ResetsAt           int64   `json:"resetsAt"`
			} `json:"secondary"`
		} `json:"rateLimits"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, fmt.Errorf("decode Codex rate limits: %w", err)
	}
	if response.RateLimits == nil {
		return nil, fmt.Errorf("Codex did not return rate limits for the current account")
	}
	out := Snapshot{}
	add := func(limit *struct {
		UsedPercent        float64 `json:"usedPercent"`
		WindowDurationMins int     `json:"windowDurationMins"`
		ResetsAt           int64   `json:"resetsAt"`
	}) {
		if limit == nil {
			return
		}
		window := ""
		switch limit.WindowDurationMins {
		case 300:
			window = WindowFiveHour
		case 10080:
			window = WindowSevenDay
		}
		if window == "" {
			return
		}
		out[window] = Window{UsedPercent: limit.UsedPercent, ResetsAt: time.Unix(limit.ResetsAt, 0)}
	}
	add(response.RateLimits.Primary)
	add(response.RateLimits.Secondary)
	if len(out) == 0 {
		return nil, fmt.Errorf("Codex did not return supported 5h or 7d rate limit windows")
	}
	return out, nil
}
