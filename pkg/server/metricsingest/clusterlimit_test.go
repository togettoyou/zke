package metricsingest

import (
	"sync"
	"testing"
	"time"
)

func testClusterLimits() ClusterLimits {
	return ClusterLimits{
		MaxSamplesPerSecond: 100,
		SampleBurstWindow:   time.Second,
		MaxActiveSeries:     1000,
		ActiveSeriesWindow:  10 * time.Minute,
	}
}

// testClock advances only when a test says so, so a rate limit can be exercised
// without the test taking as long as the window it is testing.
type testClock struct {
	now time.Time
}

func (clock *testClock) Now() time.Time { return clock.now }

func (clock *testClock) advance(step time.Duration) { clock.now = clock.now.Add(step) }

func hashes(count int) []uint64 {
	values := make([]uint64, 0, count)
	for index := range count {
		// A multiplier with high bits set spreads consecutive indexes across the
		// sketch instead of landing them in neighbouring words.
		values = append(values, uint64(index+1)*0x9e3779b97f4a7c15)
	}
	return values
}

func TestClusterLimiterRefusesBeyondSampleRateAndRecovers(t *testing.T) {
	t.Parallel()

	clock := &testClock{now: time.Unix(1_755_216_000, 0).UTC()}
	limiter := newClusterLimiter(testClusterLimits(), clock.Now)

	if reason, _, _ := limiter.admit("cluster", 100, hashes(10)); reason != "" {
		t.Fatalf("first batch inside the burst was refused: %s", reason)
	}
	reason, retry, entered := limiter.admit("cluster", 100, hashes(10))
	if reason != ThrottleReasonSampleRate {
		t.Fatalf("expected the sample rate to refuse the second batch, got %q", reason)
	}
	if !entered {
		t.Fatal("the first refusal must report the transition so it can be logged once")
	}
	if retry < minThrottleRetryAfter || retry > maxThrottleRetryAfter {
		t.Fatalf("retry hint %s is outside the clamp", retry)
	}
	if _, _, entered := limiter.admit("cluster", 100, hashes(10)); entered {
		t.Fatal("a second refusal in the same state must not report a transition again")
	}

	state, known := limiter.state("cluster")
	if !known || !state.Throttled || state.Reason != ThrottleReasonSampleRate {
		t.Fatalf("state does not report the throttling: %+v", state)
	}

	// A refused batch must not have spent tokens, so one window of quiet is
	// enough to carry the same batch again.
	clock.advance(time.Second)
	if reason, _, _ := limiter.admit("cluster", 100, hashes(10)); reason != "" {
		t.Fatalf("batch refused after the budget refilled: %s", reason)
	}
	state, _ = limiter.state("cluster")
	if state.Throttled {
		t.Fatal("state still reports throttling after an accepted batch")
	}
	if state.LastThrottledAt.IsZero() {
		t.Fatal("the last throttle time must survive recovery, or a recovered gap has no explanation")
	}
}

func TestClusterLimiterRefusesBeyondCardinality(t *testing.T) {
	t.Parallel()

	clock := &testClock{now: time.Unix(1_755_216_000, 0).UTC()}
	limits := testClusterLimits()
	limits.MaxSamplesPerSecond = 1_000_000
	limiter := newClusterLimiter(limits, clock.Now)

	if reason, _, _ := limiter.admit("cluster", 1, hashes(500)); reason != "" {
		t.Fatalf("a batch well inside the series budget was refused: %s", reason)
	}
	reason, retry, _ := limiter.admit("cluster", 1, hashes(4000))
	if reason != ThrottleReasonCardinality {
		t.Fatalf("expected the series budget to refuse the batch, got %q", reason)
	}
	if retry < minThrottleRetryAfter || retry > maxThrottleRetryAfter {
		t.Fatalf("retry hint %s is outside the clamp", retry)
	}
	state, _ := limiter.state("cluster")
	if state.ActiveSeries < 3000 {
		t.Fatalf("series estimate %d is far below what was observed", state.ActiveSeries)
	}
	if state.MaxActiveSeries != limits.MaxActiveSeries {
		t.Fatalf("state must report the limit it was measured against, got %d", state.MaxActiveSeries)
	}

	// Repeating the same series must not keep growing the estimate: a Cluster
	// reporting a stable set every scrape is not gaining cardinality.
	before := state.ActiveSeries
	limiter.admit("cluster", 1, hashes(4000))
	state, _ = limiter.state("cluster")
	if state.ActiveSeries > before+before/10 {
		t.Fatalf("estimate grew from %d to %d on a repeat of the same series", before, state.ActiveSeries)
	}
}

func TestClusterLimiterForgetsCardinalityAfterTwoQuietWindows(t *testing.T) {
	t.Parallel()

	clock := &testClock{now: time.Unix(1_755_216_000, 0).UTC()}
	limits := testClusterLimits()
	limits.MaxSamplesPerSecond = 1_000_000
	limiter := newClusterLimiter(limits, clock.Now)

	limiter.admit("cluster", 1, hashes(4000))
	if state, _ := limiter.state("cluster"); !state.Throttled {
		t.Fatal("expected the Cluster to be throttled before the windows pass")
	}

	clock.advance(2*limits.ActiveSeriesWindow + time.Second)
	if reason, _, _ := limiter.admit("cluster", 1, hashes(100)); reason != "" {
		t.Fatalf("a small batch was refused after two quiet windows: %s", reason)
	}
	state, _ := limiter.state("cluster")
	if state.Throttled {
		t.Fatal("throttling must clear once the Cluster fits its budget again")
	}
	if state.ActiveSeries > 200 {
		t.Fatalf("estimate %d still carries series from two windows ago", state.ActiveSeries)
	}
}

func TestClusterLimiterKeepsClustersApart(t *testing.T) {
	t.Parallel()

	clock := &testClock{now: time.Unix(1_755_216_000, 0).UTC()}
	limiter := newClusterLimiter(testClusterLimits(), clock.Now)

	limiter.admit("noisy", 100, hashes(10))
	if reason, _, _ := limiter.admit("noisy", 100, hashes(10)); reason == "" {
		t.Fatal("expected the noisy Cluster to be refused")
	}
	if reason, _, _ := limiter.admit("quiet", 100, hashes(10)); reason != "" {
		t.Fatalf("one Cluster's budget refused another Cluster's batch: %s", reason)
	}
	if state, known := limiter.state("quiet"); !known || state.Throttled {
		t.Fatalf("the quiet Cluster is reported as throttled: %+v", state)
	}
}

func TestClusterLimiterReportsNothingForAnUnseenCluster(t *testing.T) {
	t.Parallel()

	limiter := newClusterLimiter(
		testClusterLimits(),
		func() time.Time { return time.Unix(1_755_216_000, 0).UTC() },
	)
	if _, known := limiter.state("cluster"); known {
		t.Fatal("a Cluster that has sent nothing must not be reported as within budget")
	}
}

func TestClusterLimiterDropsLongIdleClusters(t *testing.T) {
	t.Parallel()

	clock := &testClock{now: time.Unix(1_755_216_000, 0).UTC()}
	limits := testClusterLimits()
	limiter := newClusterLimiter(limits, clock.Now)

	limiter.admit("gone", 1, hashes(10))
	clock.advance(3 * limits.ActiveSeriesWindow)
	// Any admit runs the prune, and the surviving Cluster proves the prune did
	// not simply drop everything.
	limiter.admit("present", 1, hashes(10))
	if _, known := limiter.state("gone"); known {
		t.Fatal("a Cluster idle for longer than the prune window still holds a sketch")
	}
	if _, known := limiter.state("present"); !known {
		t.Fatal("the prune removed a Cluster that had just reported")
	}
}

// Every Agent's ingest Stream is handled on its own goroutine, and several of
// them can be charging the same budget map at once. Exercised under -race
// because the sketch and the token bucket are both shared mutable state.
func TestClusterLimiterIsSafeUnderConcurrentIngest(t *testing.T) {
	t.Parallel()

	clock := &testClock{now: time.Unix(1_755_216_000, 0).UTC()}
	limits := testClusterLimits()
	limits.MaxSamplesPerSecond = 1_000_000
	limiter := newClusterLimiter(limits, clock.Now)

	var group sync.WaitGroup
	for worker := range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			clusterID := "cluster-" + string(rune('a'+worker%3))
			for range 50 {
				limiter.admit(clusterID, 10, hashes(50))
				limiter.state(clusterID)
			}
		}()
	}
	group.Wait()

	for _, suffix := range []string{"a", "b", "c"} {
		if _, known := limiter.state("cluster-" + suffix); !known {
			t.Fatalf("cluster-%s reported nothing after ingesting", suffix)
		}
	}
}

func TestSketchBitsForStaysWithinBounds(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		maxActiveSeries int
		expected        int
	}{
		{maxActiveSeries: 1, expected: minSeriesSketchBits},
		{maxActiveSeries: minSeriesSketchBits + 1, expected: minSeriesSketchBits << 1},
		{maxActiveSeries: 500_000, expected: 1 << 19},
		{maxActiveSeries: 1 << 30, expected: maxSeriesSketchBits},
	} {
		if got := sketchBitsFor(testCase.maxActiveSeries); got != testCase.expected {
			t.Fatalf(
				"sketchBitsFor(%d) = %d, want %d",
				testCase.maxActiveSeries,
				got,
				testCase.expected,
			)
		}
	}
}
