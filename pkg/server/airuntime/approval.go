package airuntime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/togettoyou/zke/pkg/server/aisession"
)

// ErrNoPendingApproval reports a decision for a call nobody is waiting on:
// already answered, already timed out, or never asked for.
var ErrNoPendingApproval = errors.New("AIOps approval request is not pending")

// pendingApprovals is the set of calls parked on a person, keyed by session and
// then by the identifier the model gave the call.
//
// Process-local, like the goroutine driving the turn. A Server that restarts
// loses the wait along with the turn, and RecoverInterrupted ends both rather
// than leaving a session showing an approval that nothing is listening for.
type pendingApprovals struct {
	mu      sync.Mutex
	waiting map[string]map[string]chan string
}

func newPendingApprovals() *pendingApprovals {
	return &pendingApprovals{waiting: make(map[string]map[string]chan string)}
}

func (pending *pendingApprovals) open(sessionID, callID string) chan string {
	answer := make(chan string, 1)
	pending.mu.Lock()
	defer pending.mu.Unlock()
	if pending.waiting[sessionID] == nil {
		pending.waiting[sessionID] = make(map[string]chan string)
	}
	pending.waiting[sessionID][callID] = answer
	return answer
}

func (pending *pendingApprovals) close(sessionID, callID string) {
	pending.mu.Lock()
	defer pending.mu.Unlock()
	if session, ok := pending.waiting[sessionID]; ok {
		delete(session, callID)
		if len(session) == 0 {
			delete(pending.waiting, sessionID)
		}
	}
}

// answer delivers one decision. It reports false when nothing is waiting, so a
// duplicate or late answer is refused rather than silently dropped.
func (pending *pendingApprovals) answer(sessionID, callID, decision string) bool {
	pending.mu.Lock()
	defer pending.mu.Unlock()
	session, ok := pending.waiting[sessionID]
	if !ok {
		return false
	}
	channel, ok := session[callID]
	if !ok {
		return false
	}
	delete(session, callID)
	if len(session) == 0 {
		delete(pending.waiting, sessionID)
	}
	channel <- decision
	return true
}

// Decide answers one parked approval request.
//
// The decision is authorized the same way every other AIOps access is: the
// caller has to still own the session and still hold `ai.run` on its Cluster.
// Approving is an act with consequences, so it is not a weaker check than
// reading the trail.
func (runtime *Runtime) Decide(
	ctx context.Context,
	sessionID, userID, callID, decision string,
	now time.Time,
) error {
	if strings.TrimSpace(callID) == "" ||
		(decision != aisession.DecisionApproved && decision != aisession.DecisionDenied) {
		return ErrInvalidInput
	}
	if _, err := runtime.Get(ctx, sessionID, userID, now); err != nil {
		return err
	}
	if !runtime.approvals.answer(sessionID, callID, decision) {
		return ErrNoPendingApproval
	}
	return nil
}
