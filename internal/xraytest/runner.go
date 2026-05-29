package xraytest

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sync/atomic"
	"time"

	xcore "github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/infra/conf/serial"
	_ "github.com/xtls/xray-core/main/distro/all" // register all xray features
)

var portCounter atomic.Int32

func init() {
	portCounter.Store(20000)
}

// nextPort returns the next available port for testing.
func nextPort() int {
	return int(portCounter.Add(1))
}

// ValidationResult holds the outcome of testing a VLESS config through xray.
type ValidationResult struct {
	IP         string
	Port       int
	Success    bool
	Latency    time.Duration // time to first byte
	Throughput float64       // bytes/sec for download test
	BytesRecv  int64
	Error      string
	Transport  string // ws, grpc, xhttp
	Retries    int    // how many attempts were needed
}

// ValidateConfig starts an xray instance with the given config, sends test
// traffic through it, and returns the result. Retries once on failure.
func ValidateConfig(ctx context.Context, cfg *VLESSConfig, timeout time.Duration) *ValidationResult {
	res := validateOnce(ctx, cfg, timeout)
	if !res.Success {
		// Retry once — DPI is flaky
		time.Sleep(500 * time.Millisecond)
		res2 := validateOnce(ctx, cfg, timeout)
		res2.Retries = 1
		if res2.Success {
			return res2
		}
		res.Retries = 1
	}
	return res
}

func validateOnce(ctx context.Context, cfg *VLESSConfig, timeout time.Duration) *ValidationResult {
	result := &ValidationResult{
		IP:        cfg.Address,
		Port:      cfg.Port,
		Transport: cfg.Network,
	}

	socksPort := nextPort()

	// Build config
	configJSON, err := BuildXrayConfig(cfg, socksPort)
	if err != nil {
		result.Error = fmt.Sprintf("build config: %v", err)
		return result
	}

	// Write temp config file (xray needs a file)
	tmpFile, err := os.CreateTemp("", "xray-test-*.json")
	if err != nil {
		result.Error = fmt.Sprintf("create temp file: %v", err)
		return result
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.Write(configJSON); err != nil {
		tmpFile.Close()
		result.Error = fmt.Sprintf("write config: %v", err)
		return result
	}
	tmpFile.Close()

	// Suppress xray stdout/stderr
	oldStdout := os.Stdout
	oldStderr := os.Stderr
	devNull, _ := os.Open(os.DevNull)
	os.Stdout = devNull
	os.Stderr = devNull

	// Start xray
	tmpFile2, err := os.Open(tmpFile.Name())
	if err != nil {
		os.Stdout = oldStdout
		os.Stderr = oldStderr
		devNull.Close()
		result.Error = fmt.Sprintf("reopen config: %v", err)
		return result
	}

	jsonConfig, err := serial.DecodeJSONConfig(tmpFile2)
	tmpFile2.Close()
	if err != nil {
		os.Stdout = oldStdout
		os.Stderr = oldStderr
		devNull.Close()
		result.Error = fmt.Sprintf("decode json config: %v", err)
		return result
	}

	pbConfig, err := jsonConfig.Build()
	if err != nil {
		os.Stdout = oldStdout
		os.Stderr = oldStderr
		devNull.Close()
		result.Error = fmt.Sprintf("build config: %v", err)
		return result
	}

	instance, err := xcore.New(pbConfig)
	if err != nil {
		result.Error = fmt.Sprintf("create instance: %v", err)
		return result
	}

	if err := instance.Start(); err != nil {
		os.Stdout = oldStdout
		os.Stderr = oldStderr
		devNull.Close()
		result.Error = fmt.Sprintf("start xray: %v", err)
		return result
	}
	defer instance.Close()

	// Restore stdout/stderr now that xray is started
	os.Stdout = oldStdout
	os.Stderr = oldStderr
	devNull.Close()

	// Wait for SOCKS port to be ready
	if !waitForPort(socksPort, 3*time.Second) {
		result.Error = "socks port not ready after 3s"
		return result
	}

	// Test through the proxy
	proxyURL := fmt.Sprintf("socks5://127.0.0.1:%d", socksPort)
	testCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	bytesRecv, err := downloadThroughProxy(testCtx, proxyURL, 1024*1024) // 1MB test
	elapsed := time.Since(start)

	if err != nil {
		result.Error = fmt.Sprintf("download: %v", err)
		result.Latency = elapsed
		return result
	}

	result.Success = true
	result.Latency = elapsed
	result.BytesRecv = bytesRecv
	if elapsed.Seconds() > 0 {
		result.Throughput = float64(bytesRecv) / elapsed.Seconds()
	}

	return result
}

// downloadThroughProxy fetches a URL through a SOCKS5 proxy and returns bytes received.
func downloadThroughProxy(ctx context.Context, proxyAddr string, bytes int64) (int64, error) {
	transport := &http.Transport{
		Proxy: func(req *http.Request) (*url.URL, error) {
			return url.Parse(proxyAddr)
		},
		DialContext: (&net.Dialer{
			Timeout: 10 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
	}

	url := fmt.Sprintf("https://speed.cloudflare.com/__down?bytes=%d", bytes)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	n, err := io.Copy(io.Discard, resp.Body)
	if err != nil {
		return n, err
	}
	return n, nil
}

// waitForPort waits until a TCP port is accepting connections.
func waitForPort(port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}
