package usageguard

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const claudeUsageURL = "https://api.anthropic.com/api/oauth/usage"

// ClaudeReader reads the OAuth credential already used by Claude Code and
// requests its subscription usage. Credential material never leaves this
// package in errors, logs, or returned values.
type ClaudeReader struct {
	HTTPClient  *http.Client
	Credentials func(context.Context, []string) (string, error)
	// CLI is the resilient fallback used when local OAuth credentials cannot be
	// read or the usage endpoint rejects them. Nil uses Claude's interactive
	// /usage panel in a bounded hidden PTY.
	CLI func(context.Context, []string) (Snapshot, error)
}

func (r ClaudeReader) Fetch(ctx context.Context, env []string) (Snapshot, error) {
	snapshot, oauthErr := r.fetchOAuth(ctx, env)
	if oauthErr == nil {
		return snapshot, nil
	}
	cli := r.CLI
	if cli == nil {
		cli = fetchClaudeUsageCLI
	}
	snapshot, cliErr := cli(ctx, env)
	if cliErr == nil {
		return snapshot, nil
	}
	return nil, fmt.Errorf("Claude usage unavailable: %w", errors.Join(oauthErr, cliErr))
}

func (r ClaudeReader) fetchOAuth(ctx context.Context, env []string) (Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	credentials := r.Credentials
	if credentials == nil {
		credentials = claudeAccessToken
	}
	token, err := credentials(ctx, env)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, claudeUsageURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build Claude usage request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("User-Agent", "orq-lite usage guard")
	client := r.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request Claude usage: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return nil, fmt.Errorf("Claude usage request returned HTTP %d", response.StatusCode)
	}
	return parseClaudeUsage(response.Body)
}

func parseClaudeUsage(body io.Reader) (Snapshot, error) {
	var response struct {
		FiveHour *struct {
			Utilization float64 `json:"utilization"`
			ResetsAt    string  `json:"resets_at"`
		} `json:"five_hour"`
		SevenDay *struct {
			Utilization float64 `json:"utilization"`
			ResetsAt    string  `json:"resets_at"`
		} `json:"seven_day"`
	}
	if err := json.NewDecoder(body).Decode(&response); err != nil {
		return nil, fmt.Errorf("decode Claude usage: %w", err)
	}
	out := Snapshot{}
	add := func(window string, usage *struct {
		Utilization float64 `json:"utilization"`
		ResetsAt    string  `json:"resets_at"`
	}) error {
		if usage == nil {
			return nil
		}
		var resetsAt time.Time
		var err error
		if usage.ResetsAt != "" {
			resetsAt, err = time.Parse(time.RFC3339, usage.ResetsAt)
			if err != nil {
				return fmt.Errorf("decode Claude %s reset time: %w", window, err)
			}
		}
		out[window] = Window{UsedPercent: usage.Utilization, ResetsAt: resetsAt}
		return nil
	}
	if err := add(WindowFiveHour, response.FiveHour); err != nil {
		return nil, err
	}
	if err := add(WindowSevenDay, response.SevenDay); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("Claude did not return 5h or 7d usage")
	}
	return out, nil
}

func claudeAccessToken(ctx context.Context, env []string) (string, error) {
	if runtime.GOOS == "darwin" {
		if token, err := claudeKeychainToken(ctx, env); err == nil && token != "" {
			return token, nil
		}
	}
	path := filepath.Join(claudeConfigDir(env), ".credentials.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read Claude credentials: %w", err)
	}
	return accessTokenFromCredentials(raw)
}

func claudeKeychainToken(ctx context.Context, env []string) (string, error) {
	user := envValue(env, "USER")
	services := []string{"Claude Code-credentials"}
	// Claude Code 2.1+ scopes the Keychain service for an explicit
	// CLAUDE_CONFIG_DIR using the first eight hex characters of its SHA-256.
	// Try it first, then retain the legacy service as a compatibility fallback.
	if configDir := envValue(env, "CLAUDE_CONFIG_DIR"); configDir != "" {
		sum := sha256.Sum256([]byte(configDir))
		services = append([]string{fmt.Sprintf("Claude Code-credentials-%x", sum[:4])}, services...)
	}
	var lastErr error
	for _, service := range services {
		args := []string{"find-generic-password", "-s", service, "-w"}
		if user != "" {
			args = append(args, "-a", user)
		}
		raw, err := exec.CommandContext(ctx, "security", args...).Output()
		if err != nil {
			lastErr = err
			continue
		}
		return accessTokenFromCredentials(raw)
	}
	return "", fmt.Errorf("read Claude credentials from keychain: %w", lastErr)
}

func accessTokenFromCredentials(raw []byte) (string, error) {
	var credentials struct {
		ClaudeAiOauth struct {
			AccessToken string `json:"accessToken"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(raw, &credentials); err != nil {
		return "", fmt.Errorf("decode Claude credentials: %w", err)
	}
	if token := strings.TrimSpace(credentials.ClaudeAiOauth.AccessToken); token != "" {
		return token, nil
	}
	return "", fmt.Errorf("Claude credentials have no access token")
}

func claudeConfigDir(env []string) string {
	if configured := envValue(env, "CLAUDE_CONFIG_DIR"); configured != "" {
		return configured
	}
	if home := envValue(env, "HOME"); home != "" {
		return filepath.Join(home, ".claude")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".claude"
	}
	return filepath.Join(home, ".claude")
}
