package pace

import (
	"math"
	"testing"
)

func near(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-6 {
		t.Fatalf("%s = %f, want %f", name, got, want)
	}
}

func TestEvenSplit(t *testing.T) {
	p := Profile{Distance: 10000, Finish: 3000}
	near(t, "start", p.Expected(0), 0)
	near(t, "half", p.Expected(5000), 1500)
	near(t, "finish", p.Expected(10000), 3000)
	near(t, "clamped", p.Expected(12000), 3000)
}

func TestPositiveAndNegativeSplit(t *testing.T) {
	for _, s := range []float64{0.05, -0.04} {
		p := Profile{Distance: 42195, Finish: 4 * 3600, Split: s}
		first := p.Expected(p.Distance / 2)
		second := p.Expected(p.Distance) - first
		near(t, "finish", p.Expected(p.Distance), p.Finish)
		near(t, "split ratio", second/first, 1+s)
	}
}

func TestStartDelayAndBand(t *testing.T) {
	p := Profile{Distance: 10000, Finish: 3000, StartDelay: 600, Uncertainty: 0.1}
	near(t, "expected", p.Expected(5000), 2100)
	near(t, "fast", p.Fast(5000), 600+1350)
	near(t, "slow", p.Slow(5000), 600+1650)
	near(t, "fast at start", p.Fast(0), 600)
}

func TestValidate(t *testing.T) {
	ok := Profile{Distance: 1, Finish: 1}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := []Profile{
		{Finish: 1},
		{Distance: 1},
		{Distance: 1, Finish: 1, Uncertainty: 1},
		{Distance: 1, Finish: 1, StartDelay: -1},
		{Distance: 1, Finish: 1, Split: 2},
	}
	for i, p := range bad {
		if p.Validate() == nil {
			t.Errorf("case %d: expected error", i)
		}
	}
}
