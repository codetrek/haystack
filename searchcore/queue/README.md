# queue

A small multi-producer, single-consumer (MPSC) async task queue. Package
`queue` is the serialization point for `searchcore` writes: the
`invertedindex`, `documents`, and related packages take a `Queue` so a consumer
can inject **one shared queue instance** across the stack, guaranteeing that all
storage mutations run on a single goroutine in submission order.

## Responsibility

- Accept work from many producers and run it on exactly one consumer goroutine,
  preserving FIFO order.
- Offer both fire-and-forget submission and synchronous (wait-for-result)
  submission.
- Provide a narrow injection interface (`Queue`) so consumers depend on the
  abstraction rather than the concrete implementation.

## Key types

### `Task`

The unit of work: anything with a `Run() error` method.

```go
type Task interface {
    Run() error
}
```

`NopeTask` is a no-op `Task` provided as a sentinel (e.g. to flush the queue).

### `Queue`

The injection interface implemented by `*Mpsc`. `searchcore` packages accept a
`Queue` rather than `*Mpsc`.

| Method | Behavior |
|--------|----------|
| `Add(task Task)` | Enqueue for async execution. Fire-and-forget; never blocks. |
| `AddFunc(fn func() error)` | Same as `Add`, adapting a plain function into a `Task`. |
| `RunTask(task Task) error` | Enqueue and block until the task has run; returns its error. |
| `RunFunc(fn func() error) error` | Enqueue a function and block until it has run; returns its error. |

**Precondition:** the queue must be started before any submission method is
called. Behavior when the queue is **not** started (never started, or stopped):

- `Add` / `AddFunc` drop the task and log; they never block.
- `RunTask` blocks indefinitely (there is no consumer to deliver its result).
- `RunFunc` returns `nil` immediately without running `fn`.

So callers must ensure `Start` has been called before submitting.

### `Mpsc`

The concrete MPSC queue and the only implementation of `Queue`.

```go
q := queue.NewMpsc("writes") // name is used only for log labelling
q.Start()                    // launches the single consumer goroutine
defer q.Stop()               // closes the channel and drains before returning
q.AddFunc(func() error { /* ... */ return nil })
```

- `NewMpsc(name)` returns an **unstarted** queue with the default buffer size of
  100.
- `SetQueueSize(size)` overrides the buffer size but must be called before
  `Start`; a non-positive size, or a call after `Start`, is ignored and logged.
- `Start()` creates the buffered channel and the single consumer goroutine that
  loops over the channel running each task in order. Calling `Start` twice is a
  no-op (logged).
- `Stop()` closes the channel and blocks until the consumer has drained the
  remaining tasks and exited. Calling `Stop` twice is a no-op (logged).

## Semantics worth noting

- **Single consumer, ordered.** Exactly one goroutine consumes the channel, so
  tasks run strictly in the order they were enqueued. This is what lets the
  search core treat the queue as its write-serialization mechanism.
- **Bounded buffer.** The channel is buffered (default 100). `Add`/`AddFunc`
  block on a *started* queue once the buffer is full, applying natural
  backpressure to producers.
- **Synchronous variants** wrap the task so the producer can block on a done
  channel until the consumer finishes and then receive the task's error.
- **Errors from `Run`** are returned to the caller only for the synchronous
  variants (`RunTask`/`RunFunc`); errors from fire-and-forget tasks are not
  surfaced by the queue.
