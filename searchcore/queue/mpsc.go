// Package queue provides a multi-producer, single-consumer async task queue.
package queue

import "log"

// Task is the unit of work dispatched through the queue.
type Task interface {
	Run() error
}

// Queue is the async task-queue injection interface; *Mpsc implements it.
// searchcore packages (invertedindex, documents, ...) take a Queue so a
// consumer can inject a single shared queue instance.
type Queue interface {
	Add(task Task)
	RunTask(task Task) error
}

// compile-time assertion: *Mpsc satisfies Queue.
var _ Queue = (*Mpsc)(nil)

// NopeTask is a no-op Task used as a sentinel (e.g. to flush the queue).
type NopeTask struct{}

func (nt *NopeTask) Run() error {
	return nil
}

type FuncTask struct {
	fn func() error
}

func (ft *FuncTask) Run() error {
	return ft.fn()
}

type FuncTaskWithArgs struct {
	fn   func(args ...interface{}) error
	args []interface{}
}

func (ft *FuncTaskWithArgs) Run() error {
	return ft.fn(ft.args...)
}

type WaitTask struct {
	task Task
	done chan error
}

func (wt *WaitTask) Run() error {
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

func NewMpsc(name string) *Mpsc {
	return &Mpsc{
		name:      name,
		queueSize: 100,
	}
}

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

func (m *Mpsc) Start() {
	// Start the queue
	if m.q != nil {
		log.Printf("[%s] Queue already started", m.name)
		return
	}
	m.q = make(chan Task, 100)
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

func (m *Mpsc) Add(task Task) {
	// Add a task to the queue
	if m.q == nil {
		log.Printf("[%s] Queue not started", m.name)
		return
	}
	m.q <- task
}

func (wq *Mpsc) RunTask(task Task) error {
	WaitTask := &WaitTask{
		task: task,
		done: make(chan error),
	}
	wq.Add(WaitTask)

	return <-WaitTask.done
}

func (m *Mpsc) AddFunc(fn func() error) {
	// Add a function to the queue
	if m.q == nil {
		log.Printf("[%s] Queue not started", m.name)
		return
	}
	m.q <- &FuncTask{fn: fn}
}

func (m *Mpsc) RunFunc(fn func() error) error {
	// Add a function to the queue and wait for it to finish
	if m.q == nil {
		log.Printf("[%s] Queue not started", m.name)
		return nil
	}

	WaitTask := &WaitTask{
		task: &FuncTask{fn: fn},
		done: make(chan error),
	}
	m.q <- WaitTask

	return <-WaitTask.done
}

func (m *Mpsc) AddFuncWithArgs(fn func(args ...interface{}) error, args ...interface{}) {
	// Add a function with arguments to the queue
	if m.q == nil {
		log.Printf("[%s] Queue not started", m.name)
		return
	}
	m.q <- &FuncTaskWithArgs{fn: fn, args: args}
}

func (m *Mpsc) RunFuncWithArgs(fn func(args ...interface{}) error, args ...interface{}) error {
	// Add a function with arguments to the queue and wait for it to finish
	if m.q == nil {
		log.Printf("[%s] Queue not started", m.name)
		return nil
	}

	WaitTask := &WaitTask{
		task: &FuncTaskWithArgs{fn: fn, args: args},
		done: make(chan error),
	}
	m.q <- WaitTask

	return <-WaitTask.done
}
