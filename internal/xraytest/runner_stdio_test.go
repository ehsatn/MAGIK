package xraytest

import (
	"errors"
	"os"
	"sync"
	"testing"
)

func TestWithSuppressedXrayOutputRestoresStdoutStderrOnError(t *testing.T) {
	origStdout := os.Stdout
	origStderr := os.Stderr
	t.Cleanup(func() {
		os.Stdout = origStdout
		os.Stderr = origStderr
	})

	sentinel := errors.New("decode failed")
	err := withSuppressedXrayOutput(func() error {
		if os.Stdout == origStdout {
			t.Fatal("stdout was not suppressed inside callback")
		}
		if os.Stderr == origStderr {
			t.Fatal("stderr was not suppressed inside callback")
		}
		return sentinel
	})

	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want sentinel", err)
	}
	if os.Stdout != origStdout {
		t.Fatal("stdout was not restored after callback error")
	}
	if os.Stderr != origStderr {
		t.Fatal("stderr was not restored after callback error")
	}
}

func TestWithSuppressedXrayOutputSerializesGlobalStdioMutation(t *testing.T) {
	origStdout := os.Stdout
	origStderr := os.Stderr
	t.Cleanup(func() {
		os.Stdout = origStdout
		os.Stderr = origStderr
	})

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := withSuppressedXrayOutput(func() error { return nil }); err != nil {
				t.Errorf("withSuppressedXrayOutput returned error: %v", err)
			}
		}()
	}
	wg.Wait()

	if os.Stdout != origStdout {
		t.Fatal("stdout was not restored after concurrent suppression")
	}
	if os.Stderr != origStderr {
		t.Fatal("stderr was not restored after concurrent suppression")
	}
}
