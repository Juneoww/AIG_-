package portscan

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tencent/AI-Infra-Guard/common/targets"
	"github.com/Tencent/AI-Infra-Guard/common/utils"
)

func TestDiscoverBoundedStableAndPreservesErrors(t *testing.T) {
	var active, peak atomic.Int32
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	config := DiscoveryConfig{Workers: 2, scan: func(ctx context.Context, host, ports string) (*utils.NmapRun, error) {
		if ports != FullTCPPorts {
			t.Errorf("coverage changed: %s", ports)
		}
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if host == "192.0.2.2" {
			return nil, errors.New("explicit failure")
		}
		return &utils.NmapRun{Hosts: []utils.Host{{Address: utils.Address{Addr: host}, Ports: utils.Ports{PortList: []utils.Port{{PortID: 8080, State: utils.State{State: "open"}}, {PortID: 8080, State: utils.State{State: "open"}}}}}}}, nil
	}}
	items := []targets.Target{{Kind: targets.KindIPv4, Value: "192.0.2.1"}, {Kind: targets.KindIPv4, Value: "192.0.2.2"}, {Kind: targets.KindIPv4, Value: "192.0.2.1"}}
	done := make(chan struct{})
	var out []targets.Target
	var failures []DiscoveryError
	var err error
	go func() {
		out, failures, err = Discover(context.Background(), items, ModeFullTCP, config, nil)
		close(done)
	}()
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("hosts still scanned serially")
		}
	}
	close(release)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("discovery did not finish")
	}
	if err != nil || len(failures) != 1 || failures[0].Target != "192.0.2.2" {
		t.Fatalf("lost failures: %v %v", err, failures)
	}
	var values []string
	for _, item := range out {
		values = append(values, item.Value)
	}
	if !reflect.DeepEqual(values, []string{"192.0.2.1", "192.0.2.1:8080", "192.0.2.2"}) {
		t.Fatalf("unstable/duplicate results: %v", values)
	}
	if peak.Load() != 2 {
		t.Fatalf("concurrency: %d", peak.Load())
	}
}

func TestDiscoverCancellationJoinsWorkers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{}, 2)
	var active atomic.Int32
	config := DiscoveryConfig{Workers: 2, scan: func(ctx context.Context, host, ports string) (*utils.NmapRun, error) {
		active.Add(1)
		defer active.Add(-1)
		started <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	var items []targets.Target
	for i := 1; i <= 100; i++ {
		items = append(items, targets.Target{Kind: targets.KindIPv4, Value: fmt.Sprintf("192.0.2.%d", i)})
	}
	done := make(chan error, 1)
	go func() { _, _, err := Discover(ctx, items, ModeFullTCP, config, nil); done <- err }()
	<-started
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel hidden: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancel blocked")
	}
	if active.Load() != 0 {
		t.Fatal("worker left behind")
	}
}

func TestDiscoveryTimeoutIsAnExplicitFailure(t *testing.T) {
	config := DiscoveryConfig{Workers: 1, Timeout: 20 * time.Millisecond, scan: func(ctx context.Context, host, ports string) (*utils.NmapRun, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	out, failed, err := Discover(context.Background(), []targets.Target{{Kind: targets.KindIPv4, Value: "192.0.2.1"}}, ModeFullTCP, config, nil)
	if err != nil || len(out) != 1 || len(failed) != 1 || !errors.Is(failed[0].Err, context.DeadlineExceeded) {
		t.Fatalf("timeout treated as no ports: %v %v %v", out, failed, err)
	}
}
