package utils

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestNmapRealFullPortCancellation(t *testing.T) {
	if os.Getenv("AIG_NMAP_INTEGRATION")!="1" {t.Skip("requires isolated nmap runtime")}
	ctx,cancel:=context.WithTimeout(context.Background(),20*time.Millisecond);defer cancel()
	start:=time.Now()
	_,err:=NmapScanContext(ctx,"127.0.0.1","1-65535")
	if !errors.Is(err,context.DeadlineExceeded){t.Fatalf("unfinished nmap hidden: %v",err)}
	if time.Since(start)>4*time.Second{t.Fatal("nmap process cleanup stalled")}
}
