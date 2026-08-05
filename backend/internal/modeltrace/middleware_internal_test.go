package modeltrace

import (
	"bytes"
	"io"
	"testing"
)

func TestRequestCaptureObserverRunsOnceAtEOF(t *testing.T) {
	body := bytes.Repeat([]byte("x"), 32*1024)
	capture := &requestCaptureReadCloser{
		ReadCloser: io.NopCloser(bytes.NewReader(body)),
		limit:      len(body),
	}

	calls := 0
	capture.setCaptureObserver(func(captured []byte) {
		calls++
		if !bytes.Equal(captured, body) {
			t.Fatalf("observer received incomplete body: got %d bytes, want %d", len(captured), len(body))
		}
	})

	buf := make([]byte, 7)
	for {
		_, err := capture.Read(buf)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read capture: %v", err)
		}
		if calls != 0 {
			t.Fatalf("observer ran before EOF after %d calls", calls)
		}
	}

	if calls != 1 {
		t.Fatalf("observer calls = %d, want 1", calls)
	}
}

func TestRequestCaptureObserverLateBindingRunsOnce(t *testing.T) {
	body := bytes.Repeat([]byte("x"), 1024)
	capture := &requestCaptureReadCloser{
		ReadCloser: io.NopCloser(bytes.NewReader(body)),
		limit:      len(body),
	}
	if _, err := io.Copy(io.Discard, capture); err != nil {
		t.Fatalf("read capture: %v", err)
	}

	calls := 0
	capture.setCaptureObserver(func(captured []byte) {
		calls++
		if !bytes.Equal(captured, body) {
			t.Fatalf("observer received incomplete body: got %d bytes, want %d", len(captured), len(body))
		}
	})
	if calls != 1 {
		t.Fatalf("observer calls = %d, want 1", calls)
	}
}

func TestRequestCaptureObserverLateBindingBeforeEOFRunsOnce(t *testing.T) {
	body := bytes.Repeat([]byte("x"), 1024)
	capture := &requestCaptureReadCloser{
		ReadCloser: io.NopCloser(bytes.NewReader(body)),
		limit:      len(body),
	}
	buf := make([]byte, len(body))
	if n, err := capture.Read(buf); n != len(body) || err != nil {
		t.Fatalf("read capture: n=%d err=%v", n, err)
	}

	calls := 0
	capture.setCaptureObserver(func(captured []byte) {
		calls++
		if !bytes.Equal(captured, body) {
			t.Fatalf("observer received incomplete body: got %d bytes, want %d", len(captured), len(body))
		}
	})
	if calls != 1 {
		t.Fatalf("observer calls = %d, want 1", calls)
	}
	if _, err := capture.Read(buf); err != io.EOF {
		t.Fatalf("final read error = %v, want EOF", err)
	}
	if calls != 1 {
		t.Fatalf("observer ran again at EOF: calls=%d", calls)
	}
}
