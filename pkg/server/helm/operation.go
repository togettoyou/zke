package helm

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/togettoyou/zke/pkg/shared/helmrelease"
)

// A release change as something that is happening, rather than as a reply.
//
// Every other Cluster write on this Server answers within one HTTP request,
// because every other one is a single object handed to an API server. A release
// change is not: it downloads a chart, renders it, applies every object an
// application owns and then — normally — waits for all of them to become ready.
// Minutes, in the ordinary case, and the operator asked for exactly that by
// leaving 等待对象就绪 switched on.
//
// Held open as one request that shape had two consequences, and both of them
// were bugs an operator hit rather than theory. The request outlived the
// Server's operation timeout and came back as a failure while the Cluster went
// on and installed the release, so a successful install was reported as an
// error and the retry after it collided with the release that was by then
// already there. And for the whole time it was open there was nothing to look
// at: no chart, no progress, no log, no way to tell a slow download from a
// Cluster that would never answer.
//
// So the request starts an operation and returns its identity, and the account
// of what is happening is a resource the Console reads while it happens. The
// deadline that bounds the work is Helm's own timeout plus the margin the
// Server needs around it, which is the only deadline that was ever the right
// one; the HTTP request no longer has an opinion about it.
//
// The record lives in this process and not in the database. It is worth exactly
// as much as the operation it describes — once that has finished and been read,
// what remains is the release itself, its revision history, and the audit event,
// all of which are stored properly. A Server restart loses the account of an
// operation, not the operation: the Agent is running it, and the release it
// produces is in the Cluster either way.

// OperationAction is which of the four release changes an operation performs.
type OperationAction string

const (
	OperationInstall   OperationAction = "install"
	OperationUpgrade   OperationAction = "upgrade"
	OperationRollback  OperationAction = "rollback"
	OperationUninstall OperationAction = "uninstall"
)

// OperationStatus is the only three-way answer there is: it is still going, it
// finished, or it failed.
type OperationStatus string

const (
	OperationRunning   OperationStatus = "running"
	OperationSucceeded OperationStatus = "succeeded"
	OperationFailed    OperationStatus = "failed"
)

// ErrOperationIdempotencyConflict is one key presented for two different
// operations. Returning the first one's account would be answering a question
// nobody asked.
var ErrOperationIdempotencyConflict = errors.New(
	"idempotency key was already used for a different Helm operation",
)

// OperationEvent is one line of the account, in the order it happened.
type OperationEvent struct {
	// Seq numbers the line within its operation and never repeats. It is what
	// makes reading the account incremental: a caller says which line it last
	// saw and is sent what came after it, instead of the whole log again every
	// time it asks.
	Seq int64     `json:"seq"`
	At  time.Time `json:"at"`
	// Stage is which part of the pipeline produced this line, so the Console
	// can show it against the step it belongs to rather than as flat text.
	Stage Stage `json:"stage"`
	// Message is empty for the line that only says a stage was entered.
	Message string `json:"message"`
}

// OperationFailure is why an operation ended without doing what it was for.
//
// It carries the same code and message the synchronous API would have returned,
// because it is read by the same Console code that reads an error response: an
// operation that failed is a failed request whose response arrived late.
type OperationFailure struct {
	// Status is the HTTP status this failure would have been returned as. It is
	// kept out of the body — the body is not an error response, it is a record
	// of one — and used by the Console only through Code.
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Operation is the whole account of one release change.
type Operation struct {
	ID          string          `json:"id"`
	ClusterID   string          `json:"cluster_id"`
	Namespace   string          `json:"namespace"`
	ReleaseName string          `json:"release_name"`
	Action      OperationAction `json:"action"`
	// DryRun says nothing will be written. It is on the operation rather than
	// only on the report, because it decides what the Console is allowed to
	// tell the operator has happened.
	DryRun       bool            `json:"dry_run"`
	Chart        string          `json:"chart,omitempty"`
	ChartVersion string          `json:"chart_version,omitempty"`
	Status       OperationStatus `json:"status"`
	// Stage is the furthest stage reached, which for a failed operation is the
	// stage it failed in.
	Stage Stage `json:"stage"`
	// Events is the account, or — when the caller asked for one — the part of
	// it after the line they already had.
	Events []OperationEvent `json:"events"`
	// EventCursor is the newest line this operation has produced, whether or not
	// it is still held. A caller sends it back as `after` and is answered with
	// whatever happened since.
	EventCursor     int64               `json:"event_cursor"`
	EventsTruncated bool                `json:"events_truncated"`
	Report          *helmrelease.Report `json:"report,omitempty"`
	Failure         *OperationFailure   `json:"failure,omitempty"`
	StartedAt       time.Time           `json:"started_at"`
	FinishedAt      *time.Time          `json:"finished_at,omitempty"`
}

// Finished reports whether there is anything left to wait for.
func (operation Operation) Finished() bool {
	return operation.Status != OperationRunning
}

// OperationSpec is what an operation is about, known before it starts.
type OperationSpec struct {
	ClusterID    string
	Namespace    string
	ReleaseName  string
	Action       OperationAction
	DryRun       bool
	Chart        string
	ChartVersion string
	// ActorUserID owns the record. Only this operator may read it back — see
	// Operations.Get.
	ActorUserID    string
	IdempotencyKey string
}

const (
	// maxOperationEvents bounds one operation's log. Helm logs a line per wait
	// poll, so a release that waits out an hour produces thousands; the ones
	// that explain what happened are the first few and the last few, and the
	// Console is told when the middle was dropped.
	maxOperationEvents = 500
	// maxOperations bounds how many accounts are kept at once. A report holds a
	// rendered manifest, which is up to a megabyte, so this is a memory budget
	// and not a queue depth — running operations are bounded by the Agent
	// admission quotas and are never evicted.
	maxOperations = 64
	// operationRetention is how long a finished operation stays readable. It
	// only has to outlive the reading of it: long enough that an operator who
	// walked away comes back to the outcome, short enough that a megabyte of
	// manifest is not held for an afternoon.
	operationRetention = 30 * time.Minute
)

type operationEntry struct {
	operation Operation
	// fingerprint is what the idempotency key was claimed for. A key presented
	// again for the same request is a retry; for a different one it is a
	// mistake worth refusing rather than answering with the wrong account.
	fingerprint string
	key         string
}

// Operations is the set of release changes this Server is running or has
// recently run.
type Operations struct {
	mutex   sync.Mutex
	entries map[string]*operationEntry
	byKey   map[string]string
	// order is insertion order, which is what eviction walks. Operations are
	// few and short-lived, so a slice beats keeping a heap correct.
	order []string
	now   func() time.Time
}

func NewOperations() *Operations {
	return &Operations{
		entries: make(map[string]*operationEntry),
		byKey:   make(map[string]string),
		now:     time.Now,
	}
}

// Start records a new operation and returns it.
//
// A key already claimed for the same request returns that operation with
// `existing` set, which is what makes a retried POST a retry: the caller shows
// the account that is already running rather than starting a second one.
func (operations *Operations) Start(spec OperationSpec) (Operation, bool, error) {
	fingerprint := strings.Join([]string{
		spec.ClusterID,
		spec.Namespace,
		spec.ReleaseName,
		string(spec.Action),
		boolText(spec.DryRun),
		spec.ActorUserID,
	}, "\n")
	identifier, err := newOperationID()
	if err != nil {
		return Operation{}, false, err
	}

	operations.mutex.Lock()
	defer operations.mutex.Unlock()
	now := operations.now()
	operations.evictLocked(now)
	if spec.IdempotencyKey != "" {
		if existingID, claimed := operations.byKey[spec.IdempotencyKey]; claimed {
			entry := operations.entries[existingID]
			if entry == nil {
				// The account expired while the key survived. Treat the key as
				// unclaimed rather than refusing a retry there is nothing left
				// to compare against.
				delete(operations.byKey, spec.IdempotencyKey)
			} else if entry.fingerprint != fingerprint {
				return Operation{}, false, ErrOperationIdempotencyConflict
			} else {
				return entry.operation.clone(0), true, nil
			}
		}
	}
	entry := &operationEntry{
		operation: Operation{
			ID:           identifier,
			ClusterID:    spec.ClusterID,
			Namespace:    spec.Namespace,
			ReleaseName:  spec.ReleaseName,
			Action:       spec.Action,
			DryRun:       spec.DryRun,
			Chart:        spec.Chart,
			ChartVersion: spec.ChartVersion,
			Status:       OperationRunning,
			// Never nil: an account with no lines yet is an empty log, and a
			// client that has to tell `[]` from `null` to know that is being
			// asked a question this Server can simply not pose.
			Events:    []OperationEvent{},
			StartedAt: now,
		},
		fingerprint: fingerprint,
		key:         spec.IdempotencyKey,
	}
	operations.entries[identifier] = entry
	operations.order = append(operations.order, identifier)
	if spec.IdempotencyKey != "" {
		operations.byKey[spec.IdempotencyKey] = identifier
	}
	return entry.operation.clone(0), false, nil
}

// Append adds one line to an operation's account and moves it to that stage.
//
// A line for an operation that has already finished, or that has been evicted,
// is dropped: the account is closed and the outcome is what matters. This is
// called from the Agent Stream's read loop, so it does the least it can.
func (operations *Operations) Append(identifier string, stage Stage, message string) {
	operations.mutex.Lock()
	defer operations.mutex.Unlock()
	entry := operations.entries[identifier]
	if entry == nil || entry.operation.Finished() {
		return
	}
	if stage != "" {
		entry.operation.Stage = stage
	}
	entry.operation.appendEvent(OperationEvent{
		At:      operations.now(),
		Stage:   stage,
		Message: message,
	})
}

// Finish closes an operation with what it produced.
//
// Exactly one of report and failure describes the outcome; a nil failure means
// it succeeded. Finishing an operation twice is impossible by construction —
// one goroutine owns it — but is ignored rather than trusted.
func (operations *Operations) Finish(
	identifier string,
	report *helmrelease.Report,
	failure *OperationFailure,
) {
	operations.mutex.Lock()
	defer operations.mutex.Unlock()
	entry := operations.entries[identifier]
	if entry == nil || entry.operation.Finished() {
		return
	}
	now := operations.now()
	// The end of one operation is the moment to release the ones before it. A
	// finished account holds a rendered manifest — up to a megabyte — and
	// nothing else in this registry runs on a timer, so retention is enforced
	// whenever it is touched. The bound that always holds is the entry count;
	// the retention window is what makes a busy Server give the memory back
	// long before reaching it.
	defer operations.evictLocked(now)
	entry.operation.FinishedAt = &now
	if failure != nil {
		entry.operation.Status = OperationFailed
		entry.operation.Failure = failure
		entry.operation.appendEvent(OperationEvent{
			At:      now,
			Stage:   entry.operation.Stage,
			Message: failure.Message,
		})
		return
	}
	entry.operation.Status = OperationSucceeded
	entry.operation.Report = report
	entry.operation.appendEvent(OperationEvent{
		At:      now,
		Stage:   entry.operation.Stage,
		Message: outcomeLine(entry.operation, report),
	})
}

// Get returns one operation's account, for the operator who started it.
//
// `after` is the last line the caller already has. Zero asks for everything,
// which is what a first read wants; anything else is answered with only what
// has happened since. That is the difference between a poll that costs a couple
// of hundred bytes and one that sends the whole log back — once a second, for
// as long as the operation runs, which for a release that waits out a rollout
// is minutes.
//
// Ownership is the whole access rule here, and it is deliberately narrower than
// the permissions that started the operation. A rendered manifest can contain a
// Secret the chart generated, and this record is polled every second or two by
// a page nobody would want an audit event for each time. Restricting it to the
// operator who already received exactly this content, from exactly this
// request, means reading it discloses nothing that was not already disclosed —
// which is what makes it right to leave unaudited.
func (operations *Operations) Get(
	identifier string,
	actorUserID string,
	after int64,
) (Operation, bool) {
	operations.mutex.Lock()
	defer operations.mutex.Unlock()
	operations.evictLocked(operations.now())
	entry := operations.entries[identifier]
	if entry == nil || !operations.ownedBy(entry, actorUserID) {
		return Operation{}, false
	}
	return entry.operation.clone(after), true
}

// List summarises this operator's operations in one Namespace, newest first.
//
// It exists so a Console that was closed mid-deployment can find its way back
// to the operation it left running, which is otherwise unreachable: the
// identity was only ever in that page's memory. The summaries carry no events
// and no report — reattaching needs the identity and the status, and the whole
// account is one request away.
func (operations *Operations) List(
	clusterID string,
	namespace string,
	actorUserID string,
) []Operation {
	operations.mutex.Lock()
	defer operations.mutex.Unlock()
	operations.evictLocked(operations.now())
	summaries := make([]Operation, 0, len(operations.order))
	for index := len(operations.order) - 1; index >= 0; index-- {
		entry := operations.entries[operations.order[index]]
		if entry == nil ||
			entry.operation.ClusterID != clusterID ||
			entry.operation.Namespace != namespace ||
			!operations.ownedBy(entry, actorUserID) {
			continue
		}
		// Everything after the newest line, which is nothing: a listing is a way
		// back to an operation, not a way to read one.
		summary := entry.operation.clone(entry.operation.EventCursor)
		summary.Report = nil
		summaries = append(summaries, summary)
	}
	return summaries
}

func (operations *Operations) ownedBy(entry *operationEntry, actorUserID string) bool {
	if actorUserID == "" {
		return false
	}
	fields := strings.Split(entry.fingerprint, "\n")
	return len(fields) == 6 && fields[5] == actorUserID
}

// evictLocked drops what is no longer worth keeping: finished operations past
// their retention, and, when there are too many records, the oldest finished
// ones. A running operation is never evicted — its account is the only place
// anyone can read what it is doing.
func (operations *Operations) evictLocked(now time.Time) {
	kept := operations.order[:0]
	for _, identifier := range operations.order {
		entry := operations.entries[identifier]
		if entry == nil {
			continue
		}
		expired := entry.operation.FinishedAt != nil &&
			now.Sub(*entry.operation.FinishedAt) > operationRetention
		if expired {
			operations.dropLocked(entry)
			continue
		}
		kept = append(kept, identifier)
	}
	operations.order = kept
	if len(operations.order) <= maxOperations {
		return
	}
	overflow := len(operations.order) - maxOperations
	kept = operations.order[:0]
	for _, identifier := range operations.order {
		entry := operations.entries[identifier]
		if entry == nil {
			continue
		}
		if overflow > 0 && entry.operation.Finished() {
			operations.dropLocked(entry)
			overflow--
			continue
		}
		kept = append(kept, identifier)
	}
	operations.order = kept
}

func (operations *Operations) dropLocked(entry *operationEntry) {
	delete(operations.entries, entry.operation.ID)
	if entry.key != "" && operations.byKey[entry.key] == entry.operation.ID {
		delete(operations.byKey, entry.key)
	}
}

// appendEvent keeps the beginning and the end of a long log and says so.
//
// The first lines are what the operation set out to do and the last are how it
// went; the thousand identical wait polls in between are the part nobody reads.
// Dropping from the middle rather than from the front keeps both ends.
func (operation *Operation) appendEvent(event OperationEvent) {
	if event.Message == "" && len(operation.Events) > 0 {
		last := operation.Events[len(operation.Events)-1]
		if last.Stage == event.Stage {
			// A stage entered twice in a row says nothing the first line did
			// not, and would push a real line out of a bounded log.
			return
		}
	}
	if len(operation.Events) >= maxOperationEvents {
		keepHead := maxOperationEvents / 4
		operation.Events = append(
			operation.Events[:keepHead],
			operation.Events[keepHead+1:]...,
		)
		operation.EventsTruncated = true
	}
	operation.EventCursor++
	event.Seq = operation.EventCursor
	operation.Events = append(operation.Events, event)
}

// clone hands out a copy carrying the lines after `after`.
//
// A copy because callers read an operation while it is still being written to,
// and a shared slice header is a data race the moment the log grows. A partial
// one because the caller usually already has the rest.
func (operation Operation) clone(after int64) Operation {
	copied := operation
	copied.Events = []OperationEvent{}
	// The retained lines are in order, so the ones being asked for are a suffix.
	// Walking from the end stops at the first line the caller already has, which
	// on a poll that arrives between two lines is the second comparison.
	first := len(operation.Events)
	for first > 0 && operation.Events[first-1].Seq > after {
		first--
	}
	if tail := operation.Events[first:]; len(tail) > 0 {
		copied.Events = make([]OperationEvent, len(tail))
		copy(copied.Events, tail)
	}
	if operation.Report != nil {
		report := *operation.Report
		copied.Report = &report
	}
	if operation.Failure != nil {
		failure := *operation.Failure
		copied.Failure = &failure
	}
	return copied
}

// outcomeLine is the last line of a successful account, in the same voice as
// the rest of the log.
func outcomeLine(operation Operation, report *helmrelease.Report) string {
	if operation.DryRun {
		return "dry run finished; nothing was written"
	}
	switch {
	case report == nil:
		return "finished"
	case operation.Action == OperationUninstall:
		return "uninstalled " + report.Name
	case report.Revision <= 0:
		return "finished"
	default:
		return "finished; " + report.Name + " is now at revision " +
			strconv.FormatInt(report.Revision, 10)
	}
}

func boolText(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

// newOperationID is a random identity rather than a sequence: an operation is
// addressable over HTTP by whoever holds its identity, and a guessable one
// would be a second way to reach an account that is otherwise restricted to the
// operator who started it.
func newOperationID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// IsOperationID is the shape a route accepts before anything is looked up.
func IsOperationID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for index := range len(value) {
		character := value[index]
		if (character >= '0' && character <= '9') ||
			(character >= 'a' && character <= 'f') {
			continue
		}
		return false
	}
	return true
}
