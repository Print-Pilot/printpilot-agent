package backoff

import (
	"testing"
	"time"
)

func TestBackoffGrowsAndCaps(t *testing.T) {
	cfg := Config{Initial: 1 * time.Second, Max: 10 * time.Second, Factor: 2.0}

	// Con jitter el valor no es estrictamente creciente, pero nunca debe
	// superar el max ni bajar del initial.
	for attempt := 0; attempt < 20; attempt++ {
		d := cfg.Next(attempt)
		if d < time.Second {
			t.Fatalf("Next(%d) = %v, no debería bajar del initial", attempt, d)
		}
		if d > 10*time.Second {
			t.Fatalf("Next(%d) = %v, superó el max", attempt, d)
		}
	}
}

func TestBackoffMaxExact(t *testing.T) {
	cfg := Config{Initial: 1 * time.Second, Max: 5 * time.Second, Factor: 2.0}
	// con factor 2, el valor exp llegaría a 16s en intento 4, debe caparse a 5s
	for attempt := 4; attempt < 20; attempt++ {
		d := cfg.Next(attempt)
		if d > 5*time.Second {
			t.Fatalf("Next(%d) = %v, superó el max 5s", attempt, d)
		}
	}
}

func TestBackoffJitterInRange(t *testing.T) {
	cfg := Config{Initial: 2 * time.Second, Max: 2 * time.Second, Factor: 2.0}
	// con initial=max, el jitter está en [initial, max]
	for attempt := 0; attempt < 50; attempt++ {
		d := cfg.Next(attempt)
		if d < 2*time.Second || d > 2*time.Second {
			t.Fatalf("Next(%d) = %v, jitter fuera de [initial,max] con initial==max", attempt, d)
		}
	}
}
