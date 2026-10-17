package mgnt

import (
	"os"
	"slices"
	"strconv"
	"testing"
	"time"
)

func startup(tb testing.TB) {
	tb.Helper()
	if err := Startup("unicorn"); err != nil {
		tb.Fatal(err)
	}
}

func BenchmarkStartup(b *testing.B) {
	b.Setenv("XDG_CONFIG_HOME", b.TempDir())
	for i := 0; i < b.N; i++ {
		startup(b)
	}
}

// TestStartupBudget keeps start-up under about 10 ms. If this is ever too
// slow, the Loader interface in internal/rules is where a compiled cache
// would go. MGNT_STARTUP_BUDGET_MS raises the limit on slow machines.
func TestStartupBudget(t *testing.T) {
	if raceEnabled {
		t.Skip("timing is meaningless under the race detector")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	budget := 10 * time.Millisecond
	if s := os.Getenv("MGNT_STARTUP_BUDGET_MS"); s != "" {
		if ms, err := strconv.Atoi(s); err == nil {
			budget = time.Duration(ms) * time.Millisecond
		}
	}
	startup(t) // warm the file cache
	var runs []time.Duration
	for i := 0; i < 15; i++ {
		t0 := time.Now()
		startup(t)
		runs = append(runs, time.Since(t0))
	}
	slices.Sort(runs)
	median := runs[len(runs)/2]
	t.Logf("start-up: median %v, best %v, worst %v", median, runs[0], runs[len(runs)-1])
	if median > budget {
		t.Errorf("start-up median %v exceeds %v", median, budget)
	}
}
