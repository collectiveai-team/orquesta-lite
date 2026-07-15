package workflow

import (
	"testing"
	"time"

	"github.com/lionelchamorro/orquestalite/internal/activity"
)

func TestDecodePolicyStrictAndRetryRules(t *testing.T) {
	policy, err := DecodePolicy([]byte(`{"maxAttempts":4,"retries":{"transient":{"maxAttempts":2,"delayMs":0}}}`))
	if err != nil {
		t.Fatal(err)
	}
	rule, ok := policy.retryRule(activity.ErrorTransient)
	if !ok || rule.MaxAttempts != 2 {
		t.Fatalf("rule=%+v %v", rule, ok)
	}
	if _, err := DecodePolicy([]byte(`{"surprise":true}`)); err == nil {
		t.Fatal("expected strict decode error")
	}
}

func TestRetryDelayBackoffIsBoundedAndDeterministic(t *testing.T) {
	runtime := &Runtime{}
	first := runtime.retryDelay(time.Second, 3, "stable-key")
	second := runtime.retryDelay(time.Second, 3, "stable-key")
	if first != second || first < 3600*time.Millisecond || first > 4400*time.Millisecond {
		t.Fatalf("delay=%s second=%s", first, second)
	}
	if capped := runtime.retryDelay(time.Minute, 20, "key"); capped < 270*time.Second || capped > 330*time.Second {
		t.Fatalf("capped delay=%s", capped)
	}
}
