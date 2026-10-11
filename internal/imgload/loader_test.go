package imgload

import (
	"errors"
	"fmt"
	"image"
	"image/png"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

// fakeDecoder records decode calls and can hold them on a gate.
type fakeDecoder struct {
	mu      sync.Mutex
	calls   map[string]int
	started chan string   // receives each path as its decode starts (if non-nil)
	gate    chan struct{} // decodes block until closed (if non-nil)
}

func newFake(l *Loader) *fakeDecoder {
	f := &fakeDecoder{calls: map[string]int{}}
	l.decode = f.decode
	return f
}

func (f *fakeDecoder) decode(path string) (*image.NRGBA, error) {
	f.mu.Lock()
	f.calls[path]++
	started, gate := f.started, f.gate
	f.mu.Unlock()
	if started != nil {
		started <- path
	}
	if gate != nil {
		<-gate
	}
	if path == "bad" {
		return nil, errors.New("broken")
	}
	// Encode the path length in the width so results are checkable.
	return image.NewNRGBA(image.Rect(0, 0, len(path), 1)), nil
}

func (f *fakeDecoder) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[path]
}

func TestLoaderCachesImagesAndErrors(t *testing.T) {
	l := NewLoader(6, 2)
	f := newFake(l)
	for range 3 {
		img, err := l.Get("abc")
		if err != nil || img.Rect.Dx() != 3 {
			t.Fatalf("Get = %v, %v", img, err)
		}
		if _, err := l.Get("bad"); err == nil {
			t.Fatal("bad: no error")
		}
	}
	if f.count("abc") != 1 || f.count("bad") != 1 {
		t.Errorf("decode calls %v, want one each", f.calls)
	}
}

func TestLoaderLRU(t *testing.T) {
	l := NewLoader(2, 1)
	f := newFake(l)
	l.Get("a")
	l.Get("b")
	l.Get("a") // a is now most recent
	l.Get("c") // evicts b
	l.Get("a")
	if f.count("a") != 1 {
		t.Errorf("a decoded %d times, want 1", f.count("a"))
	}
	l.Get("b")
	if f.count("b") != 2 {
		t.Errorf("b decoded %d times, want 2 (evicted)", f.count("b"))
	}
	l.Get("bad")
	l.Get("x")
	l.Get("y")
	l.Get("bad") // errors are evicted like images
	if f.count("bad") != 2 {
		t.Errorf("bad decoded %d times, want 2", f.count("bad"))
	}
}

func TestLoaderByteBudget(t *testing.T) {
	if NewLoader(6, 2).budget != DefaultByteBudget || DefaultByteBudget != 1536<<20 {
		t.Fatalf("default budget %d", NewLoader(6, 2).budget)
	}
	l := NewLoader(6, 1)
	f := newFake(l)
	l.SetByteBudget(40) // the fake makes len(path)×1 images: 4 bytes per path byte
	l.Get("aaaa")       // 16
	l.Get("bbbb")       // 32
	l.Get("aaaa")       // a is now most recent
	l.Get("cccc")       // 48 > 40: evicts b, the least recently used
	l.Get("aaaa")
	l.Get("cccc")
	if f.count("aaaa") != 1 || f.count("cccc") != 1 {
		t.Errorf("a or c re-decoded: %v", f.calls)
	}
	l.Get("bbbb")
	if f.count("bbbb") != 2 {
		t.Errorf("b decoded %d times, want 2 (evicted by the byte budget)", f.count("bbbb"))
	}
	l.mu.Lock()
	if l.bytes > 40 || l.bytes != int64(16*l.lru.Len()) {
		t.Errorf("accounted %d bytes for %d entries", l.bytes, l.lru.Len())
	}
	l.mu.Unlock()

	// Errors cost nothing.
	l.Get("bad")
	l.Get("bad")
	if f.count("bad") != 1 {
		t.Errorf("cached error evicted")
	}
}

func TestLoaderBudgetKeepsCurrent(t *testing.T) {
	l := NewLoader(6, 1)
	f := newFake(l)
	l.SetByteBudget(8)
	l.Get("aaaa") // 16 bytes alone exceeds the budget but is current: kept
	l.Get("aaaa")
	if f.count("aaaa") != 1 {
		t.Errorf("current image evicted: decoded %d times", f.count("aaaa"))
	}
	// A prefetch finishing later must not push the current image out.
	l.Prefetch("bbbb")
	waitIdle(t, l)
	l.Get("aaaa")
	if f.count("aaaa") != 1 {
		t.Errorf("prefetch evicted the current image")
	}
	// Invalidating drops its bytes, and the count cap still spares current.
	l.Invalidate("aaaa")
	l.mu.Lock()
	b := l.bytes
	l.mu.Unlock()
	if b != 0 {
		t.Errorf("%d bytes accounted after invalidating everything", b)
	}

	l = NewLoader(2, 1)
	f = newFake(l)
	l.Get("a")
	l.Prefetch("b", "c") // both finish after a; a stays as the current image
	waitIdle(t, l)
	l.Get("a")
	if f.count("a") != 1 {
		t.Errorf("count cap evicted the current image")
	}
}

func TestLoaderInvalidate(t *testing.T) {
	l := NewLoader(6, 2)
	f := newFake(l)
	l.Get("a")
	l.Invalidate("a")
	l.Invalidate("never-seen")
	l.Get("a")
	if f.count("a") != 2 {
		t.Errorf("a decoded %d times, want 2", f.count("a"))
	}
}

func TestLoaderInvalidateInFlight(t *testing.T) {
	l := NewLoader(6, 1)
	f := newFake(l)
	f.started = make(chan string, 1)
	f.gate = make(chan struct{})
	l.Prefetch("a")
	<-f.started
	l.Invalidate("a")
	close(f.gate)
	f.started = nil
	if _, err := l.Get("a"); err != nil { // must re-decode, not use the stale result
		t.Fatal(err)
	}
	if f.count("a") != 2 {
		t.Errorf("a decoded %d times, want 2", f.count("a"))
	}
}

func TestPrefetchDoesNotBlock(t *testing.T) {
	l := NewLoader(6, 2)
	f := newFake(l)
	f.gate = make(chan struct{})
	defer close(f.gate)
	done := make(chan struct{})
	go func() {
		l.Prefetch("a", "b", "c", "d")
		l.Prefetch("a", "e")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Prefetch blocked")
	}
}

func TestGetWaitsForInFlightPrefetch(t *testing.T) {
	l := NewLoader(6, 2)
	f := newFake(l)
	f.started = make(chan string, 1)
	f.gate = make(chan struct{})
	l.Prefetch("abc")
	<-f.started // the prefetch decode is running

	got := make(chan *image.NRGBA)
	go func() {
		img, _ := l.Get("abc")
		got <- img
	}()
	select {
	case <-got:
		t.Fatal("Get returned before the in-flight decode finished")
	case <-time.After(50 * time.Millisecond):
	}
	close(f.gate)
	if img := <-got; img == nil || img.Rect.Dx() != 3 {
		t.Fatalf("Get = %v", img)
	}
	if n := f.count("abc"); n != 1 {
		t.Errorf("decoded %d times, want 1", n)
	}
}

func TestGetTakesOverQueuedPrefetch(t *testing.T) {
	l := NewLoader(6, 1)
	f := newFake(l)
	f.started = make(chan string, 10)
	gateA := make(chan struct{})
	l.decode = func(p string) (*image.NRGBA, error) {
		if p == "a" {
			f.started <- p
			<-gateA
		}
		return f.decode(p)
	}
	l.Prefetch("a", "b") // one worker: a runs and blocks, b stays queued
	<-f.started

	done := make(chan struct{})
	go func() {
		l.Get("b")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Get(b) waited behind the prefetch queue")
	}
	close(gateA)
	l.Get("a")
	waitIdle(t, l)
	if f.count("b") != 1 || f.count("a") != 1 {
		t.Errorf("decode calls %v, want one each", f.calls)
	}
}

func TestPrefetchQueueIsBounded(t *testing.T) {
	l := NewLoader(3, 1)
	f := newFake(l)
	f.started = make(chan string, 100)
	f.gate = make(chan struct{})
	l.Prefetch("busy")
	<-f.started
	for i := range 20 {
		l.Prefetch(fmt.Sprint(i))
	}
	l.mu.Lock()
	n := len(l.queue)
	l.mu.Unlock()
	if n != 3 {
		t.Errorf("queue length %d, want 3", n)
	}
	close(f.gate)
	waitIdle(t, l)
	// Newest requests win.
	for _, p := range []string{"19", "18", "17"} {
		if f.count(p) != 1 {
			t.Errorf("%s decoded %d times, want 1", p, f.count(p))
		}
	}
	if f.count("0") != 0 {
		t.Error("stale prefetch was decoded")
	}
}

func TestLoaderNoGoroutineLeak(t *testing.T) {
	baseline := runtime.NumGoroutine()
	l := NewLoader(6, 2)
	newFake(l)
	for i := range 200 {
		l.Prefetch(fmt.Sprint(i), fmt.Sprint(i+1), fmt.Sprint(i-1))
		l.Get(fmt.Sprint(i))
	}
	waitIdle(t, l)
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > baseline {
		if time.Now().After(deadline) {
			t.Fatalf("goroutines: %d, baseline %d", runtime.NumGoroutine(), baseline)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestLoaderConcurrent(t *testing.T) {
	l := NewLoader(4, 2)
	newFake(l)
	paths := []string{"a", "bb", "ccc", "dddd", "eeeee", "ffffff", "bad"}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 500 {
				p := paths[rand.IntN(len(paths))]
				switch rand.IntN(4) {
				case 0:
					l.Prefetch(p, paths[rand.IntN(len(paths))])
				case 1:
					l.Invalidate(p)
				default:
					img, err := l.Get(p)
					if p == "bad" {
						if err == nil {
							t.Error("bad: no error")
						}
					} else if err != nil || img.Rect.Dx() != len(p) {
						t.Errorf("Get(%q) = %v, %v", p, img, err)
					}
				}
			}
		})
	}
	wg.Wait()
	waitIdle(t, l)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.lru.Len() > 4 {
		t.Errorf("lru holds %d > capacity", l.lru.Len())
	}
}

func TestLoaderRealDecode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "q.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	png.Encode(f, quadrants(8, 4))
	f.Close()
	l := NewLoader(6, 2)
	l.Prefetch(path)
	img, err := l.Get(path)
	if err != nil || img.Rect != image.Rect(0, 0, 8, 4) {
		t.Fatalf("Get = %v, %v", img, err)
	}
}

func waitIdle(t *testing.T, l *Loader) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		l.mu.Lock()
		idle := l.running == 0
		l.mu.Unlock()
		if idle {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("loader workers did not finish")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
