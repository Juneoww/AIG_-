package httpx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTaskContextCancelsInflightHTTP(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() }))
	defer server.Close()
	client, err := NewHttpx(&HTTPOptions{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client.SetContext(ctx)
	done := make(chan error, 1)
	go func() { _, err := client.Get(server.URL, nil); done <- err }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("request never started")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel hidden: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("task cancellation did not reach HTTP")
	}
}
