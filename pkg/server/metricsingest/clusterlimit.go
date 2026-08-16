package metricsingest

import (
	"math"
	"math/bits"
	"sync"
	"time"
)

const (
	DefaultMaxSamplesPerSecond = 50_000
	DefaultSampleBurstWindow   = time.Minute
	DefaultMaxActiveSeries     = 500_000
	DefaultActiveSeriesWindow  = 10 * time.Minute

	// The cardinality sketch is sized from the limit, but never smaller than
	// this — a tiny bitmap saturates immediately and would report every Cluster
	// as over budget — and never larger, so a generous configured limit cannot
	// turn into an unbounded per-Cluster allocation.
	minSeriesSketchBits = 1 << 14
	maxSeriesSketchBits = 1 << 22

	// A retry hint longer than this is not useful to tell a collector: vmagent
	// buffers on disk and retries with its own backoff, and a long sleep only
	// delays the recovery once the operator has fixed the cause.
	maxThrottleRetryAfter = time.Minute
	minThrottleRetryAfter = time.Second
)

// Throttle reasons. They are returned to the Console so an operator can tell
// "this Cluster produces too many series" from "this Cluster reports too often";
// the two have completely different fixes.
const (
	ThrottleReasonSampleRate  = "sample_rate"
	ThrottleReasonCardinality = "cardinality"
)

// ClusterLimits bound what one Cluster may send over time, as opposed to what
// one batch may carry. Batch limits alone cannot stop a Cluster that sends a
// legal batch every hundred milliseconds, or one whose label churn grows its
// series count without bound — both are how a single Cluster takes the storage
// down for every other one.
type ClusterLimits struct {
	MaxSamplesPerSecond int
	// SampleBurstWindow is how much of the rate a Cluster may spend at once. It
	// exists for reconnects: vmagent flushes its disk buffer as fast as the link
	// allows, and refusing that flush at the steady-state rate would turn every
	// brief disconnect into a long one.
	SampleBurstWindow time.Duration
	MaxActiveSeries   int
	// ActiveSeriesWindow is the span the series count is measured over. A series
	// that stopped being reported leaves the count after roughly this long.
	ActiveSeriesWindow time.Duration
}

func (limits ClusterLimits) withDefaults() ClusterLimits {
	if limits.MaxSamplesPerSecond <= 0 {
		limits.MaxSamplesPerSecond = DefaultMaxSamplesPerSecond
	}
	if limits.SampleBurstWindow <= 0 {
		limits.SampleBurstWindow = DefaultSampleBurstWindow
	}
	if limits.MaxActiveSeries <= 0 {
		limits.MaxActiveSeries = DefaultMaxActiveSeries
	}
	if limits.ActiveSeriesWindow <= 0 {
		limits.ActiveSeriesWindow = DefaultActiveSeriesWindow
	}
	return limits
}

// ClusterState is what the Server knows about one Cluster's ingest budget. It
// is reported next to the collector's own status because a data gap caused by
// the Server refusing batches looks exactly like a broken collector from the
// Console, and the operator would otherwise go looking in the wrong place.
type ClusterState struct {
	// Throttled is true while batches are currently being refused. Reason and
	// Since describe that refusal; LastThrottledAt survives it, so a gap that
	// has already recovered can still be explained.
	Throttled       bool
	Reason          string
	Since           time.Time
	LastThrottledAt time.Time
	// ActiveSeries is an estimate, not a count: it comes from a fixed-size
	// sketch so tracking a Cluster costs the same whether it reports a thousand
	// series or a million. Callers must present it as approximate.
	ActiveSeries        int
	MaxActiveSeries     int
	MaxSamplesPerSecond int
}

type clusterLimiter struct {
	limits     ClusterLimits
	sketchBits int
	now        func() time.Time

	mu        sync.Mutex
	clusters  map[string]*clusterBudget
	prunedAt  time.Time
	pruneIdle time.Duration
}

func newClusterLimiter(limits ClusterLimits, now func() time.Time) *clusterLimiter {
	limits = limits.withDefaults()
	return &clusterLimiter{
		limits:     limits,
		sketchBits: sketchBitsFor(limits.MaxActiveSeries),
		now:        now,
		clusters:   make(map[string]*clusterBudget),
		// Two windows of silence is long enough that the Cluster is gone rather
		// than between scrapes, and dropping its budget then keeps a churn of
		// short-lived Clusters from accumulating sketches forever.
		pruneIdle: 2 * limits.ActiveSeriesWindow,
	}
}

type clusterBudget struct {
	tokens   float64
	tokensAt time.Time

	sketch          []uint64
	sketchSet       int
	sketchStartedAt time.Time
	previousSeries  int

	throttled       bool
	reason          string
	since           time.Time
	lastThrottledAt time.Time
	lastSeenAt      time.Time
}

// admit reports whether one already-parsed batch fits the Cluster's budget. An
// empty reason means it does. The batch is measured either way: cardinality is
// a property of what the Cluster produces, not of what the Server chose to
// store, and forgetting refused series would let a Cluster stay just under the
// limit forever by being refused.
//
// The third return says the Cluster has just entered this state, so the caller
// can log the transition once instead of on every refused batch.
func (limiter *clusterLimiter) admit(
	clusterID string,
	samples int,
	seriesHashes []uint64,
) (string, time.Duration, bool) {
	now := limiter.now()
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	limiter.pruneLocked(now)

	budget := limiter.clusters[clusterID]
	if budget == nil {
		budget = &clusterBudget{
			tokens:          limiter.burst(),
			tokensAt:        now,
			sketch:          make([]uint64, limiter.sketchBits/64),
			sketchStartedAt: now,
		}
		limiter.clusters[clusterID] = budget
	}
	budget.lastSeenAt = now
	limiter.rotateLocked(budget, now)
	for _, hash := range seriesHashes {
		budget.observe(hash, limiter.sketchBits)
	}

	if active := limiter.activeSeriesLocked(budget, now); active > limiter.limits.MaxActiveSeries {
		// Held until the window rotates rather than re-evaluated per batch: the
		// series already counted do not disappear because the next batch is
		// smaller, and flapping between accepted and refused would produce a
		// dashed line no one can read.
		retry := limiter.limits.ActiveSeriesWindow -
			now.Sub(budget.sketchStartedAt)
		entered := budget.markThrottled(ThrottleReasonCardinality, now)
		return ThrottleReasonCardinality, clampRetryAfter(retry), entered
	}

	// Refill before spending, so a Cluster that has been quiet is not charged
	// for the time it was quiet.
	rate := float64(limiter.limits.MaxSamplesPerSecond)
	budget.tokens = math.Min(
		limiter.burst(),
		budget.tokens+rate*now.Sub(budget.tokensAt).Seconds(),
	)
	budget.tokensAt = now
	if budget.tokens < float64(samples) {
		// Tokens are not spent on a refused batch: the collector will send the
		// same samples again, and charging for both attempts would push a
		// Cluster that is merely at its limit permanently below it.
		retry := time.Duration(
			(float64(samples) - budget.tokens) / rate * float64(time.Second),
		)
		entered := budget.markThrottled(ThrottleReasonSampleRate, now)
		return ThrottleReasonSampleRate, clampRetryAfter(retry), entered
	}
	budget.tokens -= float64(samples)
	budget.throttled = false
	budget.reason = ""
	budget.since = time.Time{}
	return "", 0, false
}

// state reports a Cluster's budget without changing it. A Cluster that has not
// sent anything has no state: it is not throttled, and saying so would suggest
// the Server has looked at it.
func (limiter *clusterLimiter) state(clusterID string) (ClusterState, bool) {
	now := limiter.now()
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	budget := limiter.clusters[clusterID]
	if budget == nil {
		return ClusterState{}, false
	}
	limiter.rotateLocked(budget, now)
	return ClusterState{
		Throttled:           budget.throttled,
		Reason:              budget.reason,
		Since:               budget.since,
		LastThrottledAt:     budget.lastThrottledAt,
		ActiveSeries:        limiter.activeSeriesLocked(budget, now),
		MaxActiveSeries:     limiter.limits.MaxActiveSeries,
		MaxSamplesPerSecond: limiter.limits.MaxSamplesPerSecond,
	}, true
}

func (limiter *clusterLimiter) burst() float64 {
	return float64(limiter.limits.MaxSamplesPerSecond) *
		limiter.limits.SampleBurstWindow.Seconds()
}

// rotateLocked starts a fresh sketch once the window is over, carrying the
// previous window's estimate so the count decays across the boundary instead of
// dropping to zero and forgiving a Cluster that has changed nothing.
func (limiter *clusterLimiter) rotateLocked(budget *clusterBudget, now time.Time) {
	elapsed := now.Sub(budget.sketchStartedAt)
	if elapsed < limiter.limits.ActiveSeriesWindow {
		return
	}
	if elapsed >= 2*limiter.limits.ActiveSeriesWindow {
		// More than one whole window of silence: nothing from before describes
		// the Cluster now.
		budget.previousSeries = 0
	} else {
		budget.previousSeries = budget.estimate(limiter.sketchBits)
	}
	clear(budget.sketch)
	budget.sketchSet = 0
	budget.sketchStartedAt = now
}

func (limiter *clusterLimiter) activeSeriesLocked(
	budget *clusterBudget,
	now time.Time,
) int {
	current := budget.estimate(limiter.sketchBits)
	window := limiter.limits.ActiveSeriesWindow.Seconds()
	elapsed := now.Sub(budget.sketchStartedAt).Seconds()
	remaining := 1 - elapsed/window
	if remaining <= 0 {
		return current
	}
	return current + int(float64(budget.previousSeries)*remaining)
}

func (limiter *clusterLimiter) pruneLocked(now time.Time) {
	if now.Sub(limiter.prunedAt) < limiter.limits.ActiveSeriesWindow {
		return
	}
	limiter.prunedAt = now
	for clusterID, budget := range limiter.clusters {
		if now.Sub(budget.lastSeenAt) > limiter.pruneIdle {
			delete(limiter.clusters, clusterID)
		}
	}
}

func (budget *clusterBudget) markThrottled(reason string, now time.Time) bool {
	entered := !budget.throttled || budget.reason != reason
	if entered {
		budget.since = now
	}
	budget.throttled = true
	budget.reason = reason
	budget.lastThrottledAt = now
	return entered
}

func (budget *clusterBudget) observe(hash uint64, sketchBits int) {
	index := hash & uint64(sketchBits-1)
	word := index >> 6
	mask := uint64(1) << (index & 63)
	if budget.sketch[word]&mask != 0 {
		return
	}
	budget.sketch[word] |= mask
	budget.sketchSet++
}

// estimate is linear counting over the sketch: with m bits and V of them still
// zero, the number of distinct items that set them is -m*ln(V/m). An exact set
// would cost memory proportional to a Cluster's series count, which is the very
// thing this limit exists to bound.
func (budget *clusterBudget) estimate(sketchBits int) int {
	if budget.sketchSet == 0 {
		return 0
	}
	total := float64(sketchBits)
	zeros := total - float64(budget.sketchSet)
	if zeros < 1 {
		// Saturated. The true count is unknowable from here but it is far past
		// any limit this sketch was sized for, so report the size at which the
		// estimator stops being meaningful rather than infinity.
		zeros = 1
	}
	return int(-total * math.Log(zeros/total))
}

func sketchBitsFor(maxActiveSeries int) int {
	size := minSeriesSketchBits
	for size < maxActiveSeries && size < maxSeriesSketchBits {
		size <<= 1
	}
	if bits.OnesCount(uint(size)) != 1 {
		panic("metrics ingest sketch size must be a power of two")
	}
	return size
}

func clampRetryAfter(retry time.Duration) time.Duration {
	if retry < minThrottleRetryAfter {
		return minThrottleRetryAfter
	}
	return min(retry, maxThrottleRetryAfter)
}
