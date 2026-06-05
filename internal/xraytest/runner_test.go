package xraytest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSpeedTestTargetsPreferConfigHost(t *testing.T) {
	cfg := &VLESSConfig{
		Network: "ws",
		Host:    "worker.example.dev",
		Path:    "/ws-path",
		SNI:     "worker.example.dev",
	}
	targets := speedTestTargets(cfg, speedSampleBytes)

	if len(targets) < 3 {
		t.Fatalf("targets = %d, want at least 3", len(targets))
	}
	if !targets[0].relaxed || targets[0].url != "https://worker.example.dev/ws-path" {
		t.Fatalf("first target = %+v", targets[0])
	}
	if !strings.HasPrefix(targets[len(targets)-2].url, "https://speed.cloudflare.com/__down?bytes=") {
		t.Fatalf("speed URL missing, got %+v", targets)
	}
}

func TestSpeedBudgetReservesTimeForSpeedTest(t *testing.T) {
	got := speedBudget(20*time.Second, 900*time.Millisecond)
	if got < 8*time.Second {
		t.Fatalf("budget = %s, want at least 8s", got)
	}
}

func TestBurstProxyThroughputRequiresMinimumBytes(t *testing.T) {
	// No server here — just ensure helper handles empty work gracefully.
	bytes, tp := burstProxyThroughput(t.Context(), "socks5://127.0.0.1:1", traceProbeURL, speedSampleBytesFast)
	if bytes != 0 || tp != 0 {
		t.Fatalf("expected zero result on unreachable proxy, got bytes=%d tp=%f", bytes, tp)
	}
}

func TestProxyConnectivityCheckFallsBackToSecondTraceTarget(t *testing.T) {
	failedTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not here", http.StatusServiceUnavailable)
	}))
	defer failedTarget.Close()

	workingTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintln(w, "colo=SJC")
	}))
	defer workingTarget.Close()

	ok, latency, err := proxyConnectivityCheckTargets(
		t.Context(),
		&http.Client{Timeout: time.Second},
		[]string{failedTarget.URL, workingTarget.URL},
	)
	if err != nil {
		t.Fatalf("proxyConnectivityCheckTargets returned error: %v", err)
	}
	if !ok {
		t.Fatal("proxyConnectivityCheckTargets returned ok=false, want true")
	}
	if latency <= 0 {
		t.Fatalf("latency = %s, want positive duration", latency)
	}
}

func TestProxyConnectivityCheckReportsTraceTargetFailures(t *testing.T) {
	badBodyTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintln(w, "ip=127.0.0.1")
	}))
	defer badBodyTarget.Close()

	ok, _, err := proxyConnectivityCheckTargets(
		t.Context(),
		&http.Client{Timeout: time.Second},
		[]string{badBodyTarget.URL},
	)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if ok {
		t.Fatal("expected ok=false for trace response without colo")
	}
	if !strings.Contains(err.Error(), "no colo") {
		t.Fatalf("error = %q, want no colo context", err)
	}
}
