package tui

import "sync"

type operationTracker struct {
	mu     sync.Mutex
	cond   *sync.Cond
	next   uint64
	closed bool
	tasks  map[uint64]bool
}

type operationTicket struct {
	tracker  *operationTracker
	id       uint64
	tracked  bool
	accepted bool
}

func newOperationTracker() *operationTracker {
	tracker := &operationTracker{tasks: make(map[uint64]bool)}
	tracker.cond = sync.NewCond(&tracker.mu)
	return tracker
}

func reserveOperation(tracker *operationTracker) operationTicket {
	if tracker == nil {
		return operationTicket{accepted: true}
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if tracker.closed {
		return operationTicket{tracker: tracker, tracked: true}
	}
	tracker.next++
	tracker.tasks[tracker.next] = false
	return operationTicket{tracker: tracker, id: tracker.next, tracked: true, accepted: true}
}

func (ticket operationTicket) start() bool {
	if !ticket.tracked {
		return true
	}
	tracker := ticket.tracker
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if !ticket.accepted || tracker.closed {
		delete(tracker.tasks, ticket.id)
		tracker.cond.Broadcast()
		return false
	}
	if _, exists := tracker.tasks[ticket.id]; !exists {
		return false
	}
	tracker.tasks[ticket.id] = true
	return true
}

func (ticket operationTicket) finish() {
	if !ticket.tracked || !ticket.accepted {
		return
	}
	tracker := ticket.tracker
	tracker.mu.Lock()
	delete(tracker.tasks, ticket.id)
	tracker.cond.Broadcast()
	tracker.mu.Unlock()
}

func (tracker *operationTracker) stopAndWait() {
	if tracker == nil {
		return
	}
	tracker.mu.Lock()
	tracker.closed = true
	for id, running := range tracker.tasks {
		if !running {
			delete(tracker.tasks, id)
		}
	}
	for len(tracker.tasks) > 0 {
		tracker.cond.Wait()
	}
	tracker.mu.Unlock()
}
