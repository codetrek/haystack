// Package queue provides a multi-producer, single-consumer async task queue.
package queue

import "log"

// Task is the unit of work dispatched through the queue.
type Task interface {
	Run() error
}

// Queue is the async task-queue injection interface; *Mpsc implements it.
// core packages (invertedindex, documents, ...) take a Queue so a
// consumer can inject a single shared queue instance.
//
// Precondition: the underlying queue must be started (see *Mpsc.Start) before
// any submission method is called. The methods behave differently when the
// queue is not running:
//
//   - Add and AddFunc are fire-and-forget: if the queue has not been started
//     (or has been stopped) the task is dropped and a message is logged; they
//     never block.
//   - RunTask and RunFunc block until the submitted work has run and return its
//     error. If the queue is never started, they block indefinitely (RunFunc
//     returns nil immediately instead), so callers must ensure Start has been
//     called.
type Queue interface {
	// Add enqueues task for asynchronous execution. Fire-and-forget: drops the
	// task (and logs) if the queue is not started. Never blocks.
	Add(task Task)
	// RunTask enqueues task and blocks until it has run, returning its error.
	// The queue must be started, otherwise this blocks indefinitely.
	RunTask(task Task) error
	// AddFunc enqueues fn for asynchronous execution. Fire-and-forget: drops fn
	// (and logs) if the queue is not started. Never blocks.
	AddFunc(fn func() error)
	// RunFunc enqueues fn and blocks until it has run, returning its error. If
	// the queue is not started it returns nil immediately without running fn.
	RunFunc(fn func() error) error
}

// compile-time assertion: *Mpsc satisfies Queue.
var _ Queue = (*Mpsc)(nil)

// NopeTask is a no-op Task used as a sentinel (e.g. to flush the queue).
type NopeTask struct{}

func (nt *NopeTask) Run() error {
	return nil
}

// funcTask adapts a plain func() error into a Task.
type funcTask struct {
	fn func() error
}

func (ft *funcTask) Run() error {
	return ft.fn()
}

// waitTask wraps another Task so callers can block until it completes, using
// the done channel to deliver the wrapped task's error.
type waitTask struct {
	task Task
	done chan error
}

func (wt *waitTask) Run() error {
	defer close(wt.done)
	wt.done <- wt.task.Run()
	return nil
}

// Mpsc is a multi-producer, single-consumer queue
// that allows multiple producers to add tasks to the queue
// and a single consumer to process them in the order they were added.
type Mpsc struct {
	name      string
	queueSize int

	q    chan Task
	done chan struct{}
}

// NewMpsc returns a new, unstarted Mpsc with the given name (used for log
// labelling) and the default queue size of 100. Call Start before submitting
// tasks.
func NewMpsc(name string) *Mpsc {
	return &Mpsc{
		name:      name,
		queueSize: 100,
	}
}

// SetQueueSize sets the buffer size used when the queue is started. It must be
// called before Start: a non-positive size or a call after Start has run is
// ignored (and logged), leaving the previous size in effect.
func (m *Mpsc) SetQueueSize(size int) {
	if size <= 0 {
		log.Printf("[%s] Invalid queue size: %d", m.name, size)
		return
	}

	if m.q != nil {
		log.Printf("[%s] Changing queue size to %d, but queue already started", m.name, size)
		return
	}

	m.queueSize = size
}

// Start launches the single consumer goroutine and opens the queue for
// submissions. Calling Start on an already-started queue is a no-op (logged).
// The buffer size is queueSize (see SetQueueSize), defaulting to 100.
func (m *Mpsc) Start() {
	// Start the queue
	if m.q != nil {
		log.Printf("[%s] Queue already started", m.name)
		return
	}
	size := m.queueSize
	if size <= 0 {
		size = 100
	}
	m.q = make(chan Task, size)
	m.done = make(chan struct{})

	go func() {
		defer close(m.done)
		for {
			task, ok := <-m.q
			if !ok {
				break
			}
			task.Run()
		}
	}()
}

// Stop closes the queue and waits for the consumer to drain and exit. Calling
// Stop on an already-stopped queue is a no-op (logged).
func (m *Mpsc) Stop() {
	if m.q == nil {
		log.Printf("[%s] Queue already stopped", m.name)
		return
	}

	// Stop the queue
	close(m.q)
	<-m.done
	m.q = nil

	log.Printf("[%s] Queue stopped", m.name)
}

// Add enqueues task for asynchronous execution. It is fire-and-forget: if the
// queue is not started the task is dropped (and logged), and Add never blocks
// on a not-started queue.
func (m *Mpsc) Add(task Task) {
	// Add a task to the queue
	if m.q == nil {
		log.Printf("[%s] Queue not started", m.name)
		return
	}
	m.q <- task
}

// RunTask enqueues task and blocks until it has run, returning its error. The
// queue must be started; otherwise RunTask blocks indefinitely.
func (wq *Mpsc) RunTask(task Task) error {
	wt := &waitTask{
		task: task,
		done: make(chan error),
	}
	wq.Add(wt)

	return <-wt.done
}

// AddFunc enqueues fn for asynchronous execution. Fire-and-forget: drops fn
// (and logs) if the queue is not started; never blocks on a not-started queue.
func (m *Mpsc) AddFunc(fn func() error) {
	// Add a function to the queue
	if m.q == nil {
		log.Printf("[%s] Queue not started", m.name)
		return
	}
	m.q <- &funcTask{fn: fn}
}

// RunFunc enqueues fn and blocks until it has run, returning its error. If the
// queue is not started it returns nil immediately without running fn.
func (m *Mpsc) RunFunc(fn func() error) error {
	// Add a function to the queue and wait for it to finish
	if m.q == nil {
		log.Printf("[%s] Queue not started", m.name)
		return nil
	}

	wt := &waitTask{
		task: &funcTask{fn: fn},
		done: make(chan error),
	}
	m.q <- wt

	return <-wt.done
}
