package steward

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Approval is a mutating tool call held for an operator's decision. It is
// emitted as an EventApproval in the stream whose turn made the call; the call
// runs only once Agent.Decide approves it, with exactly these arguments.
type Approval struct {
	ID     string         `json:"id"`
	Server string         `json:"server"`
	Tool   string         `json:"tool"`
	Args   map[string]any `json:"args"`
}

// ErrNoSuchApproval is returned by Decide for an id that is not waiting: it was
// never issued, was already decided, or its turn ended.
var ErrNoSuchApproval = errors.New("no pending approval with that id")

// DefaultApprovalTimeout is how long a held call waits when Config leaves
// ApprovalTimeout zero.
const DefaultApprovalTimeout = 10 * time.Minute

type decision struct {
	ok     bool
	reason string
}

// approvalBook holds the calls waiting for a decision.
type approvalBook struct {
	timeout time.Duration
	mu      sync.Mutex
	pending map[string]chan decision
}

func newApprovalBook(timeout time.Duration) *approvalBook {
	if timeout <= 0 {
		timeout = DefaultApprovalTimeout
	}
	return &approvalBook{timeout: timeout, pending: map[string]chan decision{}}
}

type approverKey struct{}

// withApprover gives the turn running under ctx a way to put an approval in
// front of the operator. Only Stream has one — it is the only caller with a
// live connection to someone who can answer.
func withApprover(ctx context.Context, emit func(Approval)) context.Context {
	return context.WithValue(ctx, approverKey{}, emit)
}

// gate is the WriteGate every mutating tool passes through. The reason it
// returns on refusal is written for the agent, which reads it in place of a
// result: it must neither retry nor claim the change was made.
func (b *approvalBook) gate(ctx context.Context, server, tool string, args map[string]interface{}) (bool, string) {
	emit, _ := ctx.Value(approverKey{}).(func(Approval))
	if emit == nil {
		return false, fmt.Sprintf("not executed: %s changes the cluster and needs an operator's approval, "+
			"which can only be asked for in the chat panel. Tell the operator what you would do.", tool)
	}

	id := newApprovalID()
	ch := make(chan decision, 1)
	b.mu.Lock()
	b.pending[id] = ch
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
	}()

	emit(Approval{ID: id, Server: server, Tool: tool, Args: args})

	timer := time.NewTimer(b.timeout)
	defer timer.Stop()
	select {
	case d := <-ch:
		if d.ok {
			return true, ""
		}
		msg := fmt.Sprintf("not executed: the operator declined %s", tool)
		if d.reason != "" {
			msg += " (" + d.reason + ")"
		}
		return false, msg + ". Do not call it again unless the operator asks; say what you would have done instead."
	case <-timer.C:
		return false, fmt.Sprintf("not executed: the operator did not approve %s within %s. "+
			"Do not call it again unless the operator asks.", tool, b.timeout)
	case <-ctx.Done():
		return false, fmt.Sprintf("not executed: the conversation ended before the operator decided on %s.", tool)
	}
}

func (b *approvalBook) decide(id string, ok bool, reason string) error {
	b.mu.Lock()
	ch, found := b.pending[id]
	if found {
		delete(b.pending, id) // a second decision on the same call is refused
	}
	b.mu.Unlock()
	if !found {
		return ErrNoSuchApproval
	}
	ch <- decision{ok: ok, reason: reason}
	return nil
}

// Decide answers a held tool call: approve runs it with the arguments shown in
// its EventApproval; reject returns reason to the agent instead. It fails with
// ErrNoSuchApproval when nothing is waiting under id, and with an error when
// the agent was built without ApproveWrites.
func (a *Agent) Decide(id string, approve bool, reason string) error {
	if a.approvals == nil {
		return errors.New("this agent runs tools without approval")
	}
	return a.approvals.decide(id, approve, reason)
}

func newApprovalID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
