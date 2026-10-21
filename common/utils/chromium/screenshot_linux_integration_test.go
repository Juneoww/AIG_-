//go:build linux

package chromium

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// 只对本机 HTTP 服务验证真实 Chromium，离线执行不访问任何业务扫描目标。
func TestLinuxChromiumNavigationCancelReapsProcess(t *testing.T) {
	if os.Getenv("AIG_CHROMIUM_INTEGRATION")!="1" {t.Skip("requires isolated Linux Chromium runtime")}
	started:=make(chan struct{},1)
	server:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){select{case started<-struct{}{}:default:};<-r.Context().Done()}));defer server.Close()
	ctx,cancel:=context.WithTimeout(context.Background(),20*time.Second);defer cancel()
	shot,err:=NewWebScreenShotWithContext(ctx)
	if err!=nil {t.Fatal(err)}
	defer shot.Close()
	done:=make(chan error,1)
	go func(){_,err:=shot.ScreenWithContext(ctx,server.URL);done<-err}()
	select {case <-started:case <-time.After(10*time.Second):t.Fatal("Chromium never navigated to local endpoint")}
	cancel()
	select {case err:=<-done:if err==nil{t.Fatal("cancelled navigation claimed success")};case <-time.After(8*time.Second):t.Fatal("navigation did not cancel")}
	shot.Close()
	if shot.cmd.ProcessState==nil {t.Fatal("Chromium process not reaped")}
	if _,err:=os.Stat(shot.profile);!os.IsNotExist(err){t.Fatalf("browser profile leaked: %v",err)}
}

func TestLinuxChromiumCapturesLocalPage(t *testing.T) {
	if os.Getenv("AIG_CHROMIUM_INTEGRATION")!="1" {t.Skip("requires isolated Linux Chromium runtime")}
	server:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){w.Header().Set("Content-Type","text/html");_,_=w.Write([]byte("<html><title>Local evidence</title><body>Scan regression</body></html>"))}));defer server.Close()
	ctx,cancel:=context.WithTimeout(context.Background(),20*time.Second);defer cancel()
	shot,err:=NewWebScreenShotWithContext(ctx);if err!=nil {t.Fatal(err)};defer shot.Close()
	data,err:=shot.ScreenWithContext(ctx,server.URL)
	if err!=nil || len(data)<100 {t.Fatalf("real screenshot failed: bytes=%d err=%v",len(data),err)}
}
