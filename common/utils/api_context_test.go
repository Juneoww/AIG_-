package utils

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUploadFileContextCancelsResponseWait(t *testing.T) {
	stopped := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
		close(stopped)
	}))
	defer server.Close()
	file := filepath.Join(t.TempDir(), "shot.jpg")
	if err := os.WriteFile(file, []byte("jpg"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := UploadFileContext(ctx, strings.TrimPrefix(server.URL, "http://"), file)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline, got %v", err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("HTTP operation still running")
	}
}

func TestRemoteRuleLoadsAndDownloadHonorTaskCancellation(t *testing.T) {
	for _, stage := range []string{"fingerprints", "vulnerabilities", "download", "evaluation"} {
		t.Run(stage, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.Copy(io.Discard, r.Body); <-r.Context().Done() }))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			var err error
			switch stage {
			case "fingerprints":
				_, err = LoadRemoteFingerPrintsContext(ctx, strings.TrimPrefix(server.URL, "http://"))
			case "vulnerabilities":
				_, err = LoadRemoteVulStructContext(ctx, server.URL)
			case "download":
				err = DownloadFileContext(ctx, strings.TrimPrefix(server.URL, "http://"), "test", "file", filepath.Join(t.TempDir(), "target.txt"))
			case "evaluation":
				_, err = GetEvaluationsDetailContext(ctx, strings.TrimPrefix(server.URL, "http://"), "fixture")
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("%s cancellation hidden: %v", stage, err)
			}
		})
	}
}

func TestGitCloneCancelledDoesNotStartOrRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := GitCloneContext(ctx, "invalid-local-test", filepath.Join(t.TempDir(), "clone"), time.Minute); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel ignored: %v", err)
	}
}
