package runner

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Tencent/AI-Infra-Guard/common/utils/models"
)

func TestAnalysisImageContextCancelsModelRequest(t *testing.T) {
	stopped := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
		close(stopped)
	}))
	defer server.Close()
	model := models.NewOpenAI("test", "test", server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, _, err := AnalysisImageContext(ctx, "http://127.0.0.1", "html", "en", model, []byte("jpg"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected model deadline, got %v", err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("model request still running")
	}
}
