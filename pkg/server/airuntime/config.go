package airuntime

import "time"

// The AIOps loop's shipped policy.
//
// Every value here is deployment policy rather than a fact about the model
// endpoint: how long a turn may run, how far the loop may go before it is
// spending an operator's budget instead of making progress, when the context is
// compacted, and how hard a failed model request is retried. The endpoint's own
// facts — which model, how wide its context window is, how much it may emit —
// stay in the platform settings an administrator edits in the Console, because
// they change when the endpoint changes and not when the deployment does.
//
// The numbers follow the reference agent harness ZKE's runtime is modelled on:
// a compaction threshold expressed as a fraction of whatever context window the
// endpoint actually has rather than an absolute token count that is wrong for
// every other model, a verbatim recent tail kept below it, bounded parallel
// tool execution, and bounded exponential backoff with jitter on transient
// model failures.
const (
	// DefaultTurnTimeout bounds one whole turn, model calls and tool reads
	// together. A loop that has not finished by then is not going to.
	DefaultTurnTimeout = 20 * time.Minute
	// DefaultMaxSteps bounds how many times one question may go back to the
	// model. A loop is only useful if it is also guaranteed to stop; the bound
	// is generous because compaction now keeps a long investigation inside the
	// context window rather than ending it.
	DefaultMaxSteps = 40
	// DefaultMaxToolCalls bounds the whole turn, not one step. Forty steps each
	// asking for a handful of reads is a plausible investigation; several
	// hundred reads is a loop that lost the plot.
	DefaultMaxToolCalls = 120
	// DefaultMaxParallelToolCalls is how many reads one step may have in
	// flight. A step containing a mutating tool is executed serially in model
	// order; the bound therefore applies only to read-only batches and stops one
	// step from opening a Stream per object to an Agent that also serves the
	// rest of the platform.
	DefaultMaxParallelToolCalls = 10
	// DefaultRepeatedCallLimit is the convergence guard. The same tool with the
	// same arguments returning the same answer a third time is not
	// investigation.
	DefaultRepeatedCallLimit = 2
	// DefaultApprovalTimeout is how long a parked call waits for a person
	// before the turn ends. Long enough to walk away from the screen, short
	// enough that a forgotten tab does not hold a turn open for a day.
	DefaultApprovalTimeout = 15 * time.Minute
	// DefaultTitleTimeout bounds the naming call. It runs beside a turn and
	// nothing waits for it, but a model that has not answered by now is one
	// whose answer would arrive after the operator stopped reading the row.
	DefaultTitleTimeout = 30 * time.Second

	// DefaultModelRetries is how many times a transient model failure is
	// retried after the first attempt. Rate limits and 5xx from a shared
	// endpoint are the common case and are not worth ending a turn over.
	DefaultModelRetries = 5
	// DefaultModelRetryInitialDelay and DefaultModelRetryMaxDelay bound the
	// exponential backoff between those attempts.
	DefaultModelRetryInitialDelay = 500 * time.Millisecond
	DefaultModelRetryMaxDelay     = 10 * time.Second
	// DefaultModelRetryJitterRatio spreads retries from concurrent turns so a
	// recovering endpoint is not hit by all of them at the same instant.
	DefaultModelRetryJitterRatio = 0.1

	// DefaultCompactionThresholdRatio is the fraction of the endpoint's context
	// window at which the conversation is compacted before the next request.
	// Expressed as a ratio because the same deployment may be pointed at a
	// 128k model today and a 1M model tomorrow.
	DefaultCompactionThresholdRatio = 0.8
	// DefaultCompactionRetainRatio is the fraction of the context window kept
	// verbatim at the tail. Recent steps are what the next step reasons from,
	// so they survive compaction unsummarized.
	DefaultCompactionRetainRatio = 0.16
	// DefaultCompactionMaxSummaryTokens bounds the summarization call's own
	// output. A checkpoint is a structured brief, not a transcript.
	DefaultCompactionMaxSummaryTokens = 8192
	// DefaultCompactionRetries is how many extra attempts one summarization
	// gets before the runtime falls back to a mechanical summary.
	DefaultCompactionRetries = 1
	// DefaultMaxOverflowRetries is how many times a request the endpoint
	// rejected for exceeding its context window may be compacted and retried.
	DefaultMaxOverflowRetries = 1

	// DefaultMaxParallelSubtasks is how many branches one delegation may open.
	// Three covers the shape subtasks are for — resource state, recent Events,
	// metric anomaly — and keeps one question from opening a fan of model calls
	// against a shared endpoint.
	DefaultMaxParallelSubtasks = 3
	// DefaultSubtaskSteps and DefaultSubtaskToolCalls are one branch's own
	// budgets. They are much smaller than the turn's because a branch that
	// needs forty steps is not a branch: it is the investigation, and it
	// belongs on the main line where the operator can see it converge.
	DefaultSubtaskSteps     = 8
	DefaultSubtaskToolCalls = 24
	// DefaultSubtaskTimeout bounds one branch. The turn timeout still bounds
	// all of them together; this stops one stuck branch from spending the
	// whole turn while its siblings have already answered.
	DefaultSubtaskTimeout = 5 * time.Minute
)

// RetryConfig is the bounded exponential backoff applied to a transient model
// failure.
type RetryConfig struct {
	MaxRetries   int
	InitialDelay time.Duration
	MaxDelay     time.Duration
	JitterRatio  float64
}

func (retry RetryConfig) normalized() RetryConfig {
	if retry.MaxRetries < 0 {
		retry.MaxRetries = DefaultModelRetries
	}
	if retry.InitialDelay <= 0 {
		retry.InitialDelay = DefaultModelRetryInitialDelay
	}
	if retry.MaxDelay < retry.InitialDelay {
		retry.MaxDelay = max(DefaultModelRetryMaxDelay, retry.InitialDelay)
	}
	if retry.JitterRatio < 0 || retry.JitterRatio > 1 {
		retry.JitterRatio = DefaultModelRetryJitterRatio
	}
	return retry
}

// CompactionConfig is when the conversation is compacted and how much of it
// survives verbatim.
type CompactionConfig struct {
	// ThresholdRatio and RetainRatio are fractions of the endpoint's context
	// window, resolved against it on every step rather than baked into a
	// stored token count.
	ThresholdRatio float64
	RetainRatio    float64
	// MaxSummaryTokens bounds the summarization call's output.
	MaxSummaryTokens int
	// Retries is how many extra attempts one summarization gets.
	Retries int
	// MaxOverflowRetries is how many times a rejected oversized request may be
	// compacted and retried inside one step.
	MaxOverflowRetries int
}

func (compaction CompactionConfig) normalized() CompactionConfig {
	if compaction.ThresholdRatio <= 0 || compaction.ThresholdRatio > 1 {
		compaction.ThresholdRatio = DefaultCompactionThresholdRatio
	}
	if compaction.RetainRatio <= 0 || compaction.RetainRatio >= compaction.ThresholdRatio {
		compaction.RetainRatio = min(DefaultCompactionRetainRatio, compaction.ThresholdRatio/2)
	}
	if compaction.MaxSummaryTokens <= 0 {
		compaction.MaxSummaryTokens = DefaultCompactionMaxSummaryTokens
	}
	if compaction.Retries < 0 {
		compaction.Retries = DefaultCompactionRetries
	}
	if compaction.MaxOverflowRetries < 0 {
		compaction.MaxOverflowRetries = DefaultMaxOverflowRetries
	}
	return compaction
}

// SubtaskConfig bounds delegated investigation branches.
//
// A branch is a second agent loop with its own budget, so every bound the main
// loop has, a branch needs its own version of. The numbers are deliberately
// tight: subtasks exist to answer three independent questions at once, not to
// let one question become three investigations.
type SubtaskConfig struct {
	// MaxParallel is how many branches one delegation may open, and therefore
	// how many model conversations one step may start. Zero disables
	// delegation entirely and removes the tool from the catalogue.
	MaxParallel int
	// MaxSteps and MaxToolCalls bound one branch.
	MaxSteps     int
	MaxToolCalls int
	// Timeout bounds one branch inside the turn that owns it.
	Timeout time.Duration
}

func (subtask SubtaskConfig) normalized() SubtaskConfig {
	// Zero MaxParallel is a deployment saying "no delegation", which is a
	// setting rather than an omission — so only a negative value asks for the
	// default. The other three are budgets that have no meaningful zero.
	if subtask.MaxParallel < 0 {
		subtask.MaxParallel = DefaultMaxParallelSubtasks
	}
	if subtask.MaxSteps <= 0 {
		subtask.MaxSteps = DefaultSubtaskSteps
	}
	if subtask.MaxToolCalls <= 0 {
		subtask.MaxToolCalls = DefaultSubtaskToolCalls
	}
	if subtask.Timeout <= 0 {
		subtask.Timeout = DefaultSubtaskTimeout
	}
	return subtask
}

// Config is what a deployment may set about the loop.
//
// A zero value is a working configuration: every budget falls back to the
// shipped default, which keeps the runtime usable in tests and in a deployment
// that has not written an `aiops` block. The retry counts are the exception —
// zero there means "do not retry", which is a setting an operator may want, so
// only a negative value asks for the default.
type Config struct {
	TurnTimeout          time.Duration
	MaxSteps             int
	MaxToolCalls         int
	MaxParallelToolCalls int
	RepeatedCallLimit    int
	ApprovalTimeout      time.Duration
	TitleTimeout         time.Duration
	ModelRetry           RetryConfig
	Compaction           CompactionConfig
	Subtask              SubtaskConfig

	// Tools is the catalogue the model may call. A nil catalogue leaves the
	// runtime useful in tests and in a deployment that has not composed one;
	// the model is then told it has no tools rather than being left to guess.
	Tools ToolSet
	// Audit records executed tool calls and approval decisions. Nil disables
	// recording, which is only appropriate in tests.
	Audit Auditor
	// Skills is the playbook library offered alongside the catalogue. Nil
	// leaves the runtime working with no skills rather than failing, which is
	// what a test and a deployment that composed none both need.
	Skills SkillLibrary
}

func (config Config) normalized() Config {
	if config.TurnTimeout <= 0 {
		config.TurnTimeout = DefaultTurnTimeout
	}
	if config.MaxSteps <= 0 {
		config.MaxSteps = DefaultMaxSteps
	}
	if config.MaxToolCalls <= 0 {
		config.MaxToolCalls = DefaultMaxToolCalls
	}
	if config.MaxParallelToolCalls <= 0 {
		config.MaxParallelToolCalls = DefaultMaxParallelToolCalls
	}
	if config.RepeatedCallLimit <= 0 {
		config.RepeatedCallLimit = DefaultRepeatedCallLimit
	}
	if config.ApprovalTimeout <= 0 {
		config.ApprovalTimeout = DefaultApprovalTimeout
	}
	if config.TitleTimeout <= 0 {
		config.TitleTimeout = DefaultTitleTimeout
	}
	config.ModelRetry = config.ModelRetry.normalized()
	config.Compaction = config.Compaction.normalized()
	config.Subtask = config.Subtask.normalized()
	return config
}
