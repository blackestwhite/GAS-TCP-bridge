package session

import (
	"errors"
	"testing"
	"time"

	"gas-tcp-bridge/internal/protocol"
)

func TestPendingQueueBatchAndAck(t *testing.T) {
	q := NewPendingQueue(10, 1024)
	for i := uint64(1); i <= 3; i++ {
		if err := q.Enqueue(protocol.Message{SID: "s1", Seq: i, Type: protocol.TypeData, Data: protocol.EncodeBytes([]byte{byte(i)})}); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}

	batch := q.Batch(1024)
	if len(batch) != 3 {
		t.Fatalf("batch len=%d want 3", len(batch))
	}

	q.AckThrough(2)
	batch = q.Batch(1024)
	if len(batch) != 1 || batch[0].Seq != 3 {
		t.Fatalf("remaining batch=%#v want seq 3", batch)
	}
}

func TestPendingQueueDueAndMarkAttempt(t *testing.T) {
	q := NewPendingQueue(10, 1024)
	msg := protocol.Message{SID: "s1", Seq: 1, Type: protocol.TypeClose}
	if err := q.Enqueue(msg); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	now := time.Now()
	if got := q.Due(now, time.Second); len(got) != 1 {
		t.Fatalf("due before attempt len=%d want 1", len(got))
	}
	q.MarkAttempt(1, now)
	if got := q.Due(now.Add(500*time.Millisecond), time.Second); len(got) != 0 {
		t.Fatalf("due too early len=%d want 0", len(got))
	}
	if got := q.Due(now.Add(time.Second), time.Second); len(got) != 1 {
		t.Fatalf("due after retry len=%d want 1", len(got))
	}
}

func TestPendingQueueBoundsAndDuplicates(t *testing.T) {
	q := NewPendingQueue(1, 1024)
	msg := protocol.Message{SID: "s1", Seq: 1, Type: protocol.TypeClose}
	if err := q.Enqueue(msg); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := q.Enqueue(msg); err != nil {
		t.Fatalf("duplicate enqueue should be ignored: %v", err)
	}
	if q.Len() != 1 {
		t.Fatalf("len=%d want 1", q.Len())
	}
	err := q.Enqueue(protocol.Message{SID: "s1", Seq: 2, Type: protocol.TypeClose})
	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("err=%v want ErrQueueFull", err)
	}
}
