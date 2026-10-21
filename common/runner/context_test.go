package runner

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/AI-Infra-Guard/internal/options"
)

func TestEnumerationCancellationReachesHTTPAndReturns(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() }))
	defer server.Close()
	r := &Runner{Options: &options.Options{RateLimit: 2, TimeOut: 30}}
	if err := r.initStorage(); err != nil {
		t.Fatal(err)
	}
	if err := r.initComponents(); err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	r.hm.Set(server.URL, nil)
	r.total = 1
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.RunEnumerationContext(ctx) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("no HTTP request")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation hidden: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("enumeration blocked after cancellation")
	}
}

func TestRunnerInitializationBindsTaskContextBeforeLoadingRules(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() }))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opts := &options.Options{Context: ctx, RateLimit: 1, TimeOut: 30, LoadRemote: true, FPTemplates: strings.TrimPrefix(server.URL, "http://")}
	done := make(chan error, 1)
	go func() { _, err := New(opts); done <- err }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("remote initialization never started")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("initialization cancellation hidden: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("initialization blocked task shutdown")
	}
}
