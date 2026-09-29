package steward

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// startGate runs one gated call in the background and returns the approval
// it surfaced plus a channel carrying the gate's verdict.
func startGate(t *testing.T, b *approvalBook, ctx context.Context) (Approval, <-chan [2]any) {
	t.Helper()
	shown := make(chan Approval, 1)
	ctx = withApprover(ctx, func(ap Approval) { shown <- ap })
	done := make(chan [2]any, 1)
	go func() {
		ok, reason := b.gate(ctx, "sds", "sds_pool_create", map[string]any{"name": "p1"})
		done <- [2]any{ok, reason}
	}()
	select {
	case ap := <-shown:
		return ap, done
	case <-time.After(5 * time.Second):
		t.Fatal("the call was never put in front of the operator")
	}
	return Approval{}, nil
}

func TestApprovedCallRunsWithTheArgumentsShown(t *testing.T) {
	b := newApprovalBook(time.Minute)
	ap, done := startGate(t, b, context.Background())
	if ap.Tool != "sds_pool_create" || ap.Server != "sds" || ap.Args["name"] != "p1" || ap.ID == "" {
		t.Fatalf("approval does not show the call: %+v", ap)
	}
	if err := b.decide(ap.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	if v := <-done; v[0] != true {
		t.Fatalf("approved call was refused: %v", v[1])
	}
	// Decided once; a second answer is refused rather than queued.
	if err := b.decide(ap.ID, true, ""); !errors.Is(err, ErrNoSuchApproval) {
		t.Fatalf("second decision: %v", err)
	}
}

func TestRejectedCallTellsTheAgentWhy(t *testing.T) {
	b := newApprovalBook(time.Minute)
	ap, done := startGate(t, b, context.Background())
	_ = b.decide(ap.ID, false, "wrong pool")
	v := <-done
	if v[0] != false || !strings.Contains(v[1].(string), "declined") || !strings.Contains(v[1].(string), "wrong pool") {
		t.Fatalf("rejection: %v", v)
	}
}

func TestUndecidedCallTimesOutUnexecuted(t *testing.T) {
	b := newApprovalBook(50 * time.Millisecond)
	_, done := startGate(t, b, context.Background())
	if v := <-done; v[0] != false || !strings.Contains(v[1].(string), "did not approve") {
		t.Fatalf("timeout: %v", v)
	}
}

func TestClosedConversationRefusesTheCall(t *testing.T) {
	b := newApprovalBook(time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	_, done := startGate(t, b, ctx)
	cancel()
	if v := <-done; v[0] != false || !strings.Contains(v[1].(string), "conversation ended") {
		t.Fatalf("cancelled: %v", v)
	}
}

// Outside Stream there is nobody to ask, so the call is refused outright.
func TestCallWithoutAnApproverIsRefused(t *testing.T) {
	b := newApprovalBook(time.Minute)
	ok, reason := b.gate(context.Background(), "sds", "sds_pool_create", nil)
	if ok || !strings.Contains(reason, "chat panel") {
		t.Fatalf("no approver: ok=%v reason=%q", ok, reason)
	}
}
