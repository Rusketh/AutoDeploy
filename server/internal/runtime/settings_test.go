package runtime

import (
	"context"
	"testing"
)

// TestAgentPollIntervalSeconds covers the default, valid override, and the
// clamps (below-minimum and non-numeric fall back to the default). The getter
// reads only the cache, so it needs no database.
func TestAgentPollIntervalSeconds(t *testing.T) {
	s := &Settings{cache: map[string]string{}}

	if got := s.AgentPollIntervalSeconds(); got != defaultAgentPollSeconds {
		t.Errorf("unset = %d, want default %d", got, defaultAgentPollSeconds)
	}
	cases := map[string]int{
		"30":   30,                      // valid override
		"600":  600,                     // valid override
		"3":    defaultAgentPollSeconds, // below 5s minimum -> default
		"oops": defaultAgentPollSeconds, // non-numeric -> default
	}
	for raw, want := range cases {
		s.cache[keyAgentPollSeconds] = raw
		if got := s.AgentPollIntervalSeconds(); got != want {
			t.Errorf("value %q = %d, want %d", raw, got, want)
		}
	}

	// Set rejects sub-minimum values before touching the database.
	if err := s.SetAgentPollIntervalSeconds(context.Background(), 4); err == nil {
		t.Error("SetAgentPollIntervalSeconds(4) should error (< 5s)")
	}
}
