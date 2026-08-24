package usageguard

// DefaultReaders returns the built-in local provider readers. They never emit
// access tokens in errors or events; callers only receive an availability
// decision and (when configured) move to a fallback agent.
func DefaultReaders() map[string]Reader {
	return map[string]Reader{
		"codex":  CodexReader{},
		"claude": ClaudeReader{},
	}
}
