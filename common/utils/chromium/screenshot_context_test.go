package chromium

import (
	"context"
	"errors"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/cdp"
	"testing"
	"time"
)

func TestScreenWithCancelledContextDoesNotCreatePage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	shot := &WebScreenShot{}
	_, err := shot.ScreenWithContext(ctx, "http://127.0.0.1")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation: %v", err)
	}
}

type blockedCDP struct {
	calls     int
	lastError error
}

func (b *blockedCDP) Event() <-chan *cdp.Event { return nil }
func (b *blockedCDP) Call(ctx context.Context, _, _ string, _ interface{}) ([]byte, error) {
	b.calls++
	<-ctx.Done()
	b.lastError = ctx.Err()
	return nil, ctx.Err()
}

func TestScreenshotPageCreationUsesCallerDeadline(t *testing.T) {
	client := &blockedCDP{}
	shot := &WebScreenShot{browser: rod.New().Client(client)}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := shot.ScreenWithContext(ctx, "http://127.0.0.1")
	if !errors.Is(err, context.DeadlineExceeded) || client.calls != 1 {
		t.Fatalf("CDP did not receive deadline: %v calls=%d", err, client.calls)
	}
}

func TestBrowserCloseHasIndependentDeadlineAndIsIdempotent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &blockedCDP{}
	shot := &WebScreenShot{browser: rod.New().Context(ctx).Client(client), cleanupTimeout: 20 * time.Millisecond}
	shot.Close()
	shot.Close()
	if client.calls != 1 || !errors.Is(client.lastError, context.DeadlineExceeded) {
		t.Fatalf("cleanup not bounded independently: calls=%d error=%v", client.calls, client.lastError)
	}
}

func TestConstructorHonorsCancelledContextBeforeLaunching(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewWebScreenShotWithContext(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation: %v", err)
	}
}
