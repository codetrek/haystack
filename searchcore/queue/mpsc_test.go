package queue

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNewMpsc(t *testing.T) {
	m := NewMpsc("test")
	assert.NotNil(t, m)
	assert.Equal(t, "test", m.name)
	assert.Equal(t, 100, m.queueSize)
	assert.Nil(t, m.q)
}

func TestMpsc_StartStop(t *testing.T) {
	m := NewMpsc("test")
	m.Start()
	assert.NotNil(t, m.q)
	m.Stop()
	assert.Nil(t, m.q)
}

func TestMpsc_DoubleStart(t *testing.T) {
	m := NewMpsc("test")
	m.Start()
	m.Start() // should log warning, not panic
	m.Stop()
}

func TestMpsc_DoubleStop(t *testing.T) {
	m := NewMpsc("test")
	m.Start()
	m.Stop()
	m.Stop() // should log warning, not panic
}

func TestMpsc_SetQueueSize(t *testing.T) {
	m := NewMpsc("test")
	m.SetQueueSize(200)
	assert.Equal(t, 200, m.queueSize)
}

func TestMpsc_SetQueueSize_Invalid(t *testing.T) {
	m := NewMpsc("test")
	m.SetQueueSize(0)
	assert.Equal(t, 100, m.queueSize) // unchanged

	m.SetQueueSize(-1)
	assert.Equal(t, 100, m.queueSize) // unchanged
}

func TestMpsc_SetQueueSize_AfterStart(t *testing.T) {
	m := NewMpsc("test")
	m.Start()
	m.SetQueueSize(200)               // should log warning
	assert.Equal(t, 100, m.queueSize) // unchanged because already started
	m.Stop()
}

func TestMpsc_AddFunc(t *testing.T) {
	m := NewMpsc("test")
	m.Start()

	var called atomic.Bool
	m.AddFunc(func() error {
		called.Store(true)
		return nil
	})

	time.Sleep(50 * time.Millisecond)
	assert.True(t, called.Load())
	m.Stop()
}

func TestMpsc_AddFunc_NotStarted(t *testing.T) {
	m := NewMpsc("test")
	// should not panic when queue not started
	m.AddFunc(func() error { return nil })
}

func TestMpsc_RunFunc(t *testing.T) {
	m := NewMpsc("test")
	m.Start()

	var value int
	err := m.RunFunc(func() error {
		value = 42
		return nil
	})
	assert.NoError(t, err)
	assert.Equal(t, 42, value)
	m.Stop()
}

func TestMpsc_RunFunc_WithError(t *testing.T) {
	m := NewMpsc("test")
	m.Start()

	expectedErr := errors.New("test error")
	err := m.RunFunc(func() error {
		return expectedErr
	})
	assert.Equal(t, expectedErr, err)
	m.Stop()
}

func TestMpsc_RunFunc_NotStarted(t *testing.T) {
	m := NewMpsc("test")
	err := m.RunFunc(func() error { return errors.New("should not run") })
	assert.Nil(t, err) // returns nil when not started
}

func TestMpsc_Add_NotStarted(t *testing.T) {
	m := NewMpsc("test")
	m.Add(&NopeTask{}) // should not panic
}

func TestMpsc_RunTask(t *testing.T) {
	m := NewMpsc("test")
	m.Start()

	err := m.RunTask(&NopeTask{})
	assert.NoError(t, err)
	m.Stop()
}

func TestMpsc_Ordering(t *testing.T) {
	m := NewMpsc("test")
	m.Start()

	var mu sync.Mutex
	order := []int{}

	for i := 0; i < 10; i++ {
		i := i
		m.RunFunc(func() error {
			mu.Lock()
			order = append(order, i)
			mu.Unlock()
			return nil
		})
	}

	// MPSC guarantees ordering
	assert.Equal(t, []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, order)
	m.Stop()
}

func TestNopeTask(t *testing.T) {
	task := &NopeTask{}
	assert.NoError(t, task.Run())
}

func TestFuncTask(t *testing.T) {
	called := false
	task := &funcTask{fn: func() error {
		called = true
		return nil
	}}
	assert.NoError(t, task.Run())
	assert.True(t, called)
}

func TestWaitTask(t *testing.T) {
	inner := &funcTask{fn: func() error { return nil }}
	wt := &waitTask{
		task: inner,
		done: make(chan error),
	}
	go wt.Run()
	err := <-wt.done
	assert.NoError(t, err)
}
