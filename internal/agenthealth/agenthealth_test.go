package agenthealth

import (
	"reflect"
	"testing"
)

func TestMarkFailure_CrossesThreshold(t *testing.T) {
	tr := New(2)
	if got := tr.MarkFailure("a", ReasonResultMissing); got {
		t.Errorf("first failure should not skip yet")
	}
	if got := tr.MarkFailure("a", ReasonResultMissing); !got {
		t.Errorf("second failure should cross threshold and return true")
	}
	if _, ok := tr.IsSkipped("a"); !ok {
		t.Errorf("agent a should be skipped after 2 failures")
	}
}

func TestMarkSuccess_ResetsCount(t *testing.T) {
	tr := New(2)
	tr.MarkFailure("a", ReasonResultMissing)
	tr.MarkSuccess("a")
	if got := tr.MarkFailure("a", ReasonResultMissing); got {
		t.Errorf("after a success the failure count must reset")
	}
}

func TestThreshold_Zero_DisablesAutoSkip(t *testing.T) {
	tr := New(0)
	for i := 0; i < 10; i++ {
		if got := tr.MarkFailure("a", ReasonResultMissing); got {
			t.Errorf("threshold=0 must never auto-skip; iter=%d", i)
		}
	}
	if _, ok := tr.IsSkipped("a"); ok {
		t.Errorf("threshold=0 must never mark agent skipped automatically")
	}
}

func TestSkip_ExplicitBypassesThreshold(t *testing.T) {
	tr := New(5)
	tr.Skip("a", ReasonUnreachable)
	if r, ok := tr.IsSkipped("a"); !ok || r != ReasonUnreachable {
		t.Errorf("explicit Skip not honoured: reason=%q ok=%v", r, ok)
	}
}

func TestFilter_RemovesSkippedPreservesOrder(t *testing.T) {
	tr := New(1)
	tr.MarkFailure("b", ReasonResultMissing)
	got := tr.Filter([]string{"a", "b", "c", "b"})
	want := []string{"a", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Filter: got %v want %v", got, want)
	}
}

func TestMarkFailure_AlreadySkippedReturnsFalse(t *testing.T) {
	tr := New(1)
	if got := tr.MarkFailure("a", ReasonResultMissing); !got {
		t.Fatalf("first failure with threshold=1 should skip")
	}
	if got := tr.MarkFailure("a", ReasonResultMissing); got {
		t.Errorf("already-skipped agent must not return true on further failures")
	}
}

func TestSkippedAgents_IsSortedSnapshot(t *testing.T) {
	tr := New(1)
	tr.Skip("z", ReasonUnreachable)
	tr.Skip("a", ReasonRepeatedFailures)
	tr.Skip("m", ReasonResultMissing)
	got := tr.SkippedAgents()
	want := []string{"a", "m", "z"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SkippedAgents: got %v want %v", got, want)
	}
}
