// raw_http_rst_client.go sends one streaming completion request and closes its TCP
// connection with SO_LINGER=0 immediately after receiving the deterministic prefix.
// It is intentionally a raw TCP client so the gateway sees a peer RST rather than a
// client-side read timeout.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"
)

const expectedPrefix = "e2e client disconnect partial"

func main() {
	addr := flag.String("addr", "127.0.0.1:18000", "gateway TCP address")
	token := flag.String("token", "", "gateway API key")
	requestID := flag.String("request-id", "", "X-Client-Request-ID value")
	sessionID := flag.String("session-id", "", "session_id value")
	flag.Parse()

	if *token == "" || *requestID == "" || *sessionID == "" {
		fatalf("token, request-id, and session-id are required")
	}

	payload, err := json.Marshal(map[string]any{
		"model": "gpt-4",
		"messages": []map[string]string{{
			"role": "user", "content": "real client must reset after partial output",
		}},
		"session_id": *sessionID,
		"stream":     true,
	})
	if err != nil {
		fatalf("marshal request: %v", err)
	}

	conn, err := (&net.Dialer{Timeout: 3 * time.Second}).Dial("tcp", *addr)
	if err != nil {
		fatalf("dial %s: %v", *addr, err)
	}
	tcpConn, ok := conn.(*net.TCPConn)
	if !ok {
		_ = conn.Close()
		fatalf("dial %s did not return TCP connection", *addr)
	}

	req := fmt.Sprintf("POST /v1/chat/completions HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\nContent-Type: application/json\r\nX-Client-Request-ID: %s\r\nContent-Length: %d\r\nConnection: keep-alive\r\n\r\n%s", *addr, *token, *requestID, len(payload), payload)
	if err := writeAll(tcpConn, []byte(req)); err != nil {
		_ = tcpConn.Close()
		fatalf("write request: %v", err)
	}
	if err := tcpConn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		_ = tcpConn.Close()
		fatalf("set read deadline: %v", err)
	}

var responseHead bytes.Buffer
window := make([]byte, 0, 8192)
buf := make([]byte, 4096)
for {
    count, readErr := tcpConn.Read(buf)
    if count > 0 {
        chunk := buf[:count]
        if responseHead.Len() < 8192 {
            remaining := 8192 - responseHead.Len()
            if len(chunk) > remaining {
                chunk = chunk[:remaining]
            }
            _, _ = responseHead.Write(chunk)
        }
        window = append(window, buf[:count]...)
        if len(window) > 8192 {
            window = append(window[:0], window[len(window)-8192:]...)
        }
        if bytes.Contains(window, []byte(expectedPrefix)) {
            break
        }
    }
    if readErr != nil {
        _ = tcpConn.Close()
        if readErr == io.EOF {
            fatalf("gateway closed before delayed prefix")
        }
        fatalf("read delayed prefix: %v", readErr)
    }
}

responseText := responseHead.String()
if !strings.HasPrefix(responseText, "HTTP/1.1 200 ") {
    _ = tcpConn.Close()
    fatalf("gateway response is not HTTP 200: %q", firstLine(responseText))
}
if !bytes.Contains(window, []byte(expectedPrefix)) {
    _ = tcpConn.Close()
    fatalf("gateway response omitted delayed prefix")
}
	if err := tcpConn.SetLinger(0); err != nil {
		_ = tcpConn.Close()
		fatalf("set SO_LINGER=0: %v", err)
	}
	if err := tcpConn.Close(); err != nil {
		fatalf("close RST connection: %v", err)
	}
	fmt.Printf("RST_CLIENT_OK addr=%s request_id=%s received_prefix=1\n", *addr, *requestID)
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		count, err := writer.Write(data)
		if err != nil {
			return err
		}
		data = data[count:]
	}
	return nil
}

func firstLine(value string) string {
	if index := strings.Index(value, "\r\n"); index >= 0 {
		return value[:index]
	}
	return value
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[raw-http-rst-client][ERROR] "+format+"\n", args...)
	os.Exit(1)
}
