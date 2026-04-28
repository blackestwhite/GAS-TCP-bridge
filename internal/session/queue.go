package session

import (
	"errors"
	"sync"
	"time"

	"gas-tcp-bridge/internal/protocol"
)

var ErrQueueFull = errors.New("queue full")

type PendingQueue struct {
	mu        sync.Mutex
	maxChunks int
	maxBytes  int
	bytes     int
	chunks    []protocol.Message
	seen      map[uint64]struct{}
	sentAt    map[uint64]time.Time
	notify    chan struct{}
}

func NewPendingQueue(maxChunks int, maxBytes int) *PendingQueue {
	if maxChunks <= 0 {
		maxChunks = 1
	}
	if maxBytes <= 0 {
		maxBytes = 1
	}
	return &PendingQueue{
		maxChunks: maxChunks,
		maxBytes:  maxBytes,
		seen:      make(map[uint64]struct{}),
		sentAt:    make(map[uint64]time.Time),
		notify:    make(chan struct{}, 1),
	}
}

func (q *PendingQueue) Enqueue(msg protocol.Message) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if _, ok := q.seen[msg.Seq]; ok {
		return nil
	}
	size := protocol.ApproxPayloadSize(msg)
	if len(q.chunks) >= q.maxChunks || q.bytes+size > q.maxBytes {
		return ErrQueueFull
	}
	q.chunks = append(q.chunks, msg)
	q.seen[msg.Seq] = struct{}{}
	q.bytes += size
	q.signalLocked()
	return nil
}

func (q *PendingQueue) AckThrough(seq uint64) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if seq == 0 || len(q.chunks) == 0 {
		return
	}
	dst := q.chunks[:0]
	for _, msg := range q.chunks {
		if msg.Seq <= seq {
			delete(q.seen, msg.Seq)
			delete(q.sentAt, msg.Seq)
			q.bytes -= protocol.ApproxPayloadSize(msg)
			continue
		}
		dst = append(dst, msg)
	}
	q.chunks = dst
	if q.bytes < 0 {
		q.bytes = 0
	}
}

func (q *PendingQueue) Batch(maxBytes int) []protocol.Message {
	q.mu.Lock()
	defer q.mu.Unlock()

	var out []protocol.Message
	used := 0
	for _, msg := range q.chunks {
		size := protocol.ApproxPayloadSize(msg)
		if len(out) > 0 && used+size > maxBytes {
			break
		}
		out = append(out, msg)
		used += size
		if maxBytes > 0 && used >= maxBytes {
			break
		}
	}
	return append([]protocol.Message(nil), out...)
}

func (q *PendingQueue) Due(now time.Time, retryAfter time.Duration) []protocol.Message {
	q.mu.Lock()
	defer q.mu.Unlock()

	var out []protocol.Message
	for _, msg := range q.chunks {
		last, sent := q.sentAt[msg.Seq]
		if !sent || now.Sub(last) >= retryAfter {
			out = append(out, msg)
		}
	}
	return out
}

func (q *PendingQueue) MarkAttempt(seq uint64, at time.Time) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, ok := q.seen[seq]; ok {
		q.sentAt[seq] = at
	}
}

func (q *PendingQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.chunks)
}

func (q *PendingQueue) Notify() <-chan struct{} {
	return q.notify
}

func (q *PendingQueue) signalLocked() {
	select {
	case q.notify <- struct{}{}:
	default:
	}
}
