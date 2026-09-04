package usageguard

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/creack/pty"
)

const (
	claudeCLITimeout   = 25 * time.Second
	claudeStartupDelay = 2 * time.Second
	claudeSettleDelay  = 2 * time.Second
	claudeOutputLimit  = 100_000
)

var (
	claudeSessionLabel = regexp.MustCompile(`(?i)current\s*session`)
	claudeWeeklyLabel  = regexp.MustCompile(`(?i)(?:current\s*week|weekly\s*(?:limits?|usage|rate\s*limits?)|7\s*[- ]?\s*day)`)
	claudePercent      = regexp.MustCompile(`(?i)(\d{1,3}(?:\.\d+)?)\s*%\s*(used|consumed|left|remaining|available)`)
	claudeTrustPrompt  = regexp.MustCompile(`(?i)(?:do you trust|trust the files|safety check)`)
	claudeUsagePalette = regexp.MustCompile(`(?i)(?:show plan|usage limits)`)
	terminalCSI        = regexp.MustCompile("\\x1b\\[[0-9;?]*[ -/]*[@-~]")
	terminalOSC        = regexp.MustCompile("\\x1b\\][^\\x07]*(?:\\x07|\\x1b\\\\)")
)

// fetchClaudeUsageCLI asks Claude's own interactive /usage panel. Besides
// covering installations where the OAuth usage endpoint is unavailable, this
// lets Claude refresh or otherwise manage its own credentials before reporting
// the subscription windows.
func fetchClaudeUsageCLI(ctx context.Context, env []string) (Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, claudeCLITimeout)
	defer cancel()

	probeDir := filepath.Join(os.TempDir(), "orq-lite-rate-limit-pty", "cwd")
	if err := os.MkdirAll(probeDir, 0o700); err != nil {
		return nil, fmt.Errorf("create Claude usage probe directory: %w", err)
	}

	cmd := exec.CommandContext(ctx, "claude")
	cmd.Dir = probeDir
	cmd.Env = append(effectiveEnv(env), "TERM=xterm-256color")
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 120, Rows: 40})
	if err != nil {
		return nil, fmt.Errorf("start Claude usage CLI: %w", err)
	}
	defer terminal.Close()

	chunks := make(chan []byte, 8)
	readDone := make(chan error, 1)
	go func() {
		buffer := make([]byte, 4096)
		for {
			n, readErr := terminal.Read(buffer)
			if n > 0 {
				chunk := append([]byte(nil), buffer[:n]...)
				select {
				case chunks <- chunk:
				case <-ctx.Done():
					readDone <- ctx.Err()
					return
				}
			}
			if readErr != nil {
				readDone <- readErr
				return
			}
		}
	}()

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	startup := time.NewTimer(claudeStartupDelay)
	defer startup.Stop()
	var settle *time.Timer
	var settleC <-chan time.Time
	var enterTicker *time.Ticker
	var enterC <-chan time.Time
	var output string
	sentUsage := false
	confirmedPalette := false
	trusted := false
	processExited := false
	defer func() {
		if settle != nil {
			settle.Stop()
		}
		if enterTicker != nil {
			enterTicker.Stop()
		}
	}()

	finish := func(reason error) (Snapshot, error) {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = terminal.Close()
		if !processExited {
			select {
			case <-exited:
			case <-time.After(time.Second):
			}
		}
		snapshot, parseErr := parseClaudeCLIUsage(output)
		if parseErr == nil {
			return snapshot, nil
		}
		if reason == nil {
			reason = parseErr
		}
		clean := stripTerminalControlSequences(output)
		lower := strings.ToLower(clean)
		return nil, fmt.Errorf(
			"Claude /usage did not return subscription limits (bytes=%d, session_label=%t, weekly_label=%t, percent=%t, palette=%t, usage_text=%t, trust=%t, login=%t, failed=%t): %w",
			len(output), claudeSessionLabel.MatchString(clean), claudeWeeklyLabel.MatchString(clean), claudePercent.MatchString(clean), claudeUsagePalette.MatchString(clean), strings.Contains(lower, "usage"), claudeTrustPrompt.MatchString(clean), strings.Contains(lower, "login"), strings.Contains(lower, "failed"), reason,
		)
	}

	for {
		select {
		case <-ctx.Done():
			return finish(ctx.Err())
		case <-startup.C:
			sentUsage = true
			_, _ = terminal.Write([]byte("/usage\r"))
			enterTicker = time.NewTicker(800 * time.Millisecond)
			enterC = enterTicker.C
		case <-enterC:
			if settle == nil {
				_, _ = terminal.Write([]byte("\r"))
			}
		case chunk := <-chunks:
			output += string(chunk)
			if len(output) > claudeOutputLimit {
				output = output[len(output)-claudeOutputLimit:]
			}
			clean := stripTerminalControlSequences(output)
			if !trusted && claudeTrustPrompt.MatchString(clean) {
				trusted = true
				_, _ = terminal.Write([]byte("y\r"))
				if sentUsage {
					sentUsage = false
					if enterTicker != nil {
						enterTicker.Stop()
						enterTicker = nil
						enterC = nil
					}
					startup.Reset(time.Second)
				}
			}
			if sentUsage && !confirmedPalette && claudeUsagePalette.MatchString(clean) {
				confirmedPalette = true
				_, _ = terminal.Write([]byte("\r"))
			}
			if sentUsage && settle == nil {
				_, parseErr := parseClaudeCLIUsage(clean)
				if parseErr != nil {
					continue
				}
				settle = time.NewTimer(claudeSettleDelay)
				settleC = settle.C
			}
		case <-settleC:
			return finish(nil)
		case exitErr := <-exited:
			processExited = true
			return finish(exitErr)
		case readErr := <-readDone:
			if ctx.Err() != nil {
				return finish(ctx.Err())
			}
			return finish(readErr)
		}
	}
}

func parseClaudeCLIUsage(output string) (Snapshot, error) {
	lines := strings.FieldsFunc(stripTerminalControlSequences(output), func(r rune) bool { return r == '\r' || r == '\n' })
	out := Snapshot{}
	if percent, ok := percentAfterLabel(lines, claudeSessionLabel, false); ok {
		out[WindowFiveHour] = Window{UsedPercent: percent}
	}
	if percent, ok := percentAfterLabel(lines, claudeWeeklyLabel, true); ok {
		out[WindowSevenDay] = Window{UsedPercent: percent}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("usage panel contained no 5h or 7d percentages")
	}
	return out, nil
}

func percentAfterLabel(lines []string, label *regexp.Regexp, excludeFable bool) (Percent, bool) {
	for i, line := range lines {
		if !label.MatchString(line) || (excludeFable && strings.Contains(strings.ToLower(line), "fable")) {
			continue
		}
		for j := i; j < min(i+12, len(lines)); j++ {
			if j > i && (claudeSessionLabel.MatchString(lines[j]) || claudeWeeklyLabel.MatchString(lines[j])) {
				break
			}
			match := claudePercent.FindStringSubmatch(lines[j])
			if len(match) != 3 {
				continue
			}
			raw, err := strconv.ParseFloat(match[1], 64)
			if err != nil {
				continue
			}
			// The panel labels the number as either consumption or headroom;
			// the label decides which converter applies.
			var percent Percent
			switch strings.ToLower(match[2]) {
			case "left", "remaining", "available":
				percent, err = Remaining(raw)
			default:
				percent, err = Used(raw)
			}
			if err != nil {
				continue
			}
			return percent, true
		}
	}
	return 0, false
}

func stripTerminalControlSequences(output string) string {
	return terminalCSI.ReplaceAllString(terminalOSC.ReplaceAllString(output, ""), "")
}

func effectiveEnv(env []string) []string {
	if len(env) == 0 {
		return os.Environ()
	}
	return append([]string(nil), env...)
}
