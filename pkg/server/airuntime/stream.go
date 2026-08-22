package airuntime

import "sync"

// StreamEvent is a live signal about a running turn.
//
// Two things travel this way and they are not the same kind of fact. A wake
// tells a watcher that new durable entries exist and it should read them; a
// delta is model output on its way to the screen before the step it belongs to
// has finished. Only the first is a record. The delta exists so an operator
// sees the answer forming instead of a spinner, and the durable `model` entry
// that follows is what the trail, the export and any later review are built
// from.
type StreamEvent struct {
	// Type is "entries" for a wake, "delta" for streamed answer text,
	// "reasoning" for streamed reasoning summary text, and "reset" for a step
	// whose partial output must be discarded because the request is being sent
	// again.
	Type string
	Turn int32
	Step int
	Text string
}

const (
	StreamEntries   = "entries"
	StreamDelta     = "delta"
	StreamReasoning = "reasoning"
	// StreamReset tells a watcher to drop what it has of the current step.
	//
	// A retried model call starts its answer over, and text from the attempt
	// that failed halfway would otherwise stay on screen with the new answer
	// appended to it. Nothing durable is involved: the step's `model` entry is
	// written once, from the attempt that actually succeeded.
	StreamReset = "reset"
)

// streamSubscriberBuffer is how far one watcher may fall behind before its
// deltas start being dropped.
//
// Dropping is correct for a delta and would not be for an entry: the durable
// trail is read by sequence, so a watcher that missed live text still ends up
// with the finished step. A slow reader therefore loses some typing animation,
// never a fact.
const streamSubscriberBuffer = 256

// broker fans live signals out to whoever is watching a session.
//
// Process-local on purpose. Durable delivery is the trajectory table, which a
// reconnect resumes from by sequence and which survives a Server restart; this
// only makes the same information arrive sooner.
type broker struct {
	mu          sync.Mutex
	subscribers map[string]map[int64]chan StreamEvent
	nextID      int64
}

func newBroker() *broker {
	return &broker{subscribers: make(map[string]map[int64]chan StreamEvent)}
}

// Subscribe returns a channel of live signals for one session and the function
// that releases it. The caller must call the release function.
func (runtime *Runtime) Subscribe(sessionID string) (<-chan StreamEvent, func()) {
	return runtime.stream.subscribe(sessionID)
}

func (broker *broker) subscribe(sessionID string) (<-chan StreamEvent, func()) {
	channel := make(chan StreamEvent, streamSubscriberBuffer)
	broker.mu.Lock()
	broker.nextID++
	id := broker.nextID
	if broker.subscribers[sessionID] == nil {
		broker.subscribers[sessionID] = make(map[int64]chan StreamEvent)
	}
	broker.subscribers[sessionID][id] = channel
	broker.mu.Unlock()
	return channel, func() {
		broker.mu.Lock()
		defer broker.mu.Unlock()
		if session, ok := broker.subscribers[sessionID]; ok {
			delete(session, id)
			if len(session) == 0 {
				delete(broker.subscribers, sessionID)
			}
		}
		close(channel)
	}
}

func (broker *broker) publish(sessionID string, event StreamEvent) {
	broker.mu.Lock()
	defer broker.mu.Unlock()
	for _, channel := range broker.subscribers[sessionID] {
		select {
		case channel <- event:
		default:
			// See streamSubscriberBuffer: a watcher too slow for live text
			// still reads every entry from the durable trail.
		}
	}
}
