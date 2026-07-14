package job

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestKeyedMutexSerializesSameKey asserts that two goroutines holding the same
// key never overlap in the critical section.
func TestKeyedMutexSerializesSameKey(t *testing.T) {
	km := newKeyedMutex()
	var (
		active   int32
		maxSeen  int32
		wg       sync.WaitGroup
	)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			km.Lock(42)
			n := atomic.AddInt32(&active, 1)
			for {
				m := atomic.LoadInt32(&maxSeen)
				if n <= m || atomic.CompareAndSwapInt32(&maxSeen, m, n) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			atomic.AddInt32(&active, -1)
			km.Unlock(42)
		}()
	}
	wg.Wait()
	if maxSeen != 1 {
		t.Fatalf("max concurrent holders of key 42 = %d, want 1", maxSeen)
	}
}

// TestKeyedMutexDifferentKeysConcurrent asserts distinct keys do not block each
// other: two goroutines on different keys both enter before either releases.
func TestKeyedMutexDifferentKeysConcurrent(t *testing.T) {
	km := newKeyedMutex()
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	for _, key := range []int64{1, 2} {
		go func(k int64) {
			km.Lock(k)
			entered <- struct{}{}
			<-release
			km.Unlock(k)
		}(key)
	}
	// Both must enter without either releasing.
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("distinct keys blocked each other (deadlock/serialization)")
		}
	}
	close(release)
}

func TestKeyedMutexReusableAfterUnlock(t *testing.T) {
	km := newKeyedMutex()
	km.Lock(7)
	km.Unlock(7)
	done := make(chan struct{})
	go func() {
		km.Lock(7)
		km.Unlock(7)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("key not reusable after Unlock")
	}
}
