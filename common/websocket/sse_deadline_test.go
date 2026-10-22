package websocket

import (
	"errors"
	"fmt"
	"net"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

type boundedSSEWriter struct {
	*httptest.ResponseRecorder
	mu       sync.Mutex
	deadline time.Time
	gate     chan struct{}
	entered  chan struct{}
	once     sync.Once
	active   atomic.Int32
	overlap  atomic.Bool
}

func newBoundedSSEWriter() *boundedSSEWriter {
	return &boundedSSEWriter{ResponseRecorder: httptest.NewRecorder()}
}
func (w *boundedSSEWriter) SetWriteDeadline(deadline time.Time) error {
	w.mu.Lock()
	w.deadline = deadline
	w.mu.Unlock()
	return nil
}
func (w *boundedSSEWriter) Write(data []byte) (int, error) {
	if w.active.Add(1) != 1 {
		w.overlap.Store(true)
	}
	defer w.active.Add(-1)
	if w.entered != nil {
		w.once.Do(func() { close(w.entered) })
	}
	w.mu.Lock()
	deadline := w.deadline
	w.mu.Unlock()
	if w.gate != nil {
		if deadline.IsZero() {
			<-w.gate
		} else {
			timer := time.NewTimer(time.Until(deadline))
			defer timer.Stop()
			select {
			case <-w.gate:
			case <-timer.C:
				return 0, os.ErrDeadlineExceeded
			}
		}
	}
	time.Sleep(time.Millisecond)
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ResponseRecorder.Write(data)
}
func (w *boundedSSEWriter) Flush() {}

func TestSSEInitialSendDoesNotHoldManagerLock(t *testing.T) {
	t.Setenv("AIG_SSE_WRITE_TIMEOUT", "200ms")
	sm := NewSSEManager()
	writer := newBoundedSSEWriter()
	writer.gate = make(chan struct{})
	writer.entered = make(chan struct{})
	result := make(chan error, 1)
	go func() { result <- sm.AddConnection("slow", "u", writer) }()
	<-writer.entered
	counted := make(chan int, 1)
	go func() { counted <- sm.GetConnectionCount() }()
	select {
	case <-counted:
	case <-time.After(50 * time.Millisecond):
		t.Error("initial SSE network write held global manager lock")
	}
	close(writer.gate)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	sm.RemoveConnection("slow")
}

func TestSSEWritesAreSerialized(t *testing.T) {
	sm := NewSSEManager()
	writer := newBoundedSSEWriter()
	if err := sm.AddConnection("task", "u", writer); err != nil {
		t.Fatal(err)
	}
	defer sm.RemoveConnection("task")
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := sm.SendEvent(fmt.Sprint(i), "task", "liveStatus", map[string]int{"sequence": i}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if writer.overlap.Load() {
		t.Fatal("SSE writes overlap on one response writer")
	}
}

func TestSSERejectsUnboundedWriter(t *testing.T) {
	sm := NewSSEManager()
	if err := sm.AddConnection("unsupported", "u", httptest.NewRecorder()); err == nil {
		sm.RemoveConnection("unsupported")
		t.Fatal("writer without deadline support accepted")
	}
	if sm.HasConnection("unsupported") {
		t.Fatal("unsupported connection left registered")
	}
}

func TestSSEStaleCleanupPreservesReplacement(t *testing.T) {
	sm := NewSSEManager()
	old, err := sm.addConnection("same", "u", newBoundedSSEWriter())
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := sm.addConnection("same", "u", newBoundedSSEWriter())
	if err != nil {
		t.Fatal(err)
	}
	defer sm.RemoveConnection("same")
	sm.removeConnection(old)
	sm.removeConnection(old)
	if sm.connections["same"] != replacement {
		t.Fatal("old SSE cleanup removed replacement")
	}
	select {
	case <-old.CloseChan:
	default:
		t.Fatal("old subscriber was not closed")
	}
	select {
	case <-replacement.CloseChan:
		t.Fatal("replacement subscriber closed by stale cleanup")
	default:
	}
}

func TestSSESlowHTTPReaderIsBoundedThroughGinWriter(t *testing.T) {
	t.Setenv("AIG_SSE_WRITE_TIMEOUT", "100ms")
	sm := NewSSEManager()
	result := make(chan error, 1)
	route := gin.New()
	route.GET("/events", func(c *gin.Context) {
		if err := sm.AddConnection("slow-http", "u", c.Writer); err != nil {
			result <- err
			return
		}
		result <- sm.SendEvent("large", "slow-http", "liveStatus", map[string]string{"text": strings.Repeat("x", 32*1024*1024)})
	})
	server := httptest.NewServer(route)
	defer server.Close()
	peer, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	if tcp, ok := peer.(*net.TCPConn); ok {
		if err := tcp.SetReadBuffer(1024); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fmt.Fprintf(peer, "GET /events HTTP/1.1\r\nHost: localhost\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err == nil || !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("slow write did not return deadline error: %v", err)
		}
	case <-time.After(3 * time.Second):
		peer.Close()
		<-result
		t.Fatal("slow HTTP subscriber blocked beyond configured write deadline")
	}
	if sm.HasConnection("slow-http") {
		t.Fatal("failed slow subscriber remained registered")
	}
}
