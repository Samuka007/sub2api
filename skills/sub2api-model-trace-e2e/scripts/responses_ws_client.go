// responses_ws_client.go drives the public Responses WebSocket ingress without
// depending on a third-party client. It is intentionally limited to the
// deterministic local e2e fixture protocol.
package main

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const websocketMagic = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

type wsClient struct {
	connection net.Conn
	reader     *bufio.Reader
	writer     *bufio.Writer
}

type responseEnvelope struct {
	Type       string `json:"type"`
	ResponseID string `json:"response_id"`
	Delta      string `json:"delta"`
	Response   struct {
		ID string `json:"id"`
	} `json:"response"`
}

func main() {
	endpoint := flag.String("url", "", "Responses WebSocket URL")
	token := flag.String("token", "", "local API key")
	requestID := flag.String("request-id", "", "connection request ID")
	mode := flag.String("mode", "multi", "multi, pause, or disconnect")
	readyFile := flag.String("ready-file", "", "created after the first terminal event")
	continueFile := flag.String("continue-file", "", "required before sending turn two in pause mode")
	flag.Parse()

	if *endpoint == "" || *token == "" || *requestID == "" {
		fatalf("url, token, and request-id are required")
	}
	client, err := dial(*endpoint, *token, *requestID)
	if err != nil {
		fatalf("dial: %v", err)
	}
	defer func() { _ = client.connection.Close() }()

	firstInput := "e2e ws turn one"
	if *mode == "disconnect" {
		firstInput = "e2e ws-disconnect"
	}
	firstPayload := fmt.Sprintf(`{"type":"response.create","model":"gpt-e2e-ws","input":%q,"stream":true}`, firstInput)
	if err := client.writeText(firstPayload); err != nil {
		fatalf("write first turn: %v", err)
	}
	firstID, err := client.readThroughTerminal(1, *mode == "disconnect")
	if err != nil {
		fatalf("read first turn: %v", err)
	}
	if *mode == "disconnect" {
		fmt.Printf("WS_DISCONNECTED connection_request_id=%s partial_response_id=%s\n", *requestID, firstID)
		return
	}
	fmt.Printf("WS_TURN connection_request_id=%s turn_index=1 response_id=%s\n", *requestID, firstID)

	if *mode == "pause" {
		if *readyFile == "" || *continueFile == "" {
			fatalf("pause mode requires ready-file and continue-file")
		}
		if err := os.WriteFile(*readyFile, []byte("ready\n"), 0o600); err != nil {
			fatalf("write ready file: %v", err)
		}
		deadline := time.Now().Add(20 * time.Second)
		for {
			if _, err := os.Stat(*continueFile); err == nil {
				break
			}
			if time.Now().After(deadline) {
				fatalf("timed out waiting for continue file")
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	second := fmt.Sprintf(`{"type":"response.create","model":"gpt-e2e-ws","previous_response_id":%q,"input":"e2e ws turn two","stream":true}`, firstID)
	if err := client.writeText(second); err != nil {
		fatalf("write second turn: %v", err)
	}
	secondID, err := client.readThroughTerminal(2, false)
	if err != nil {
		fatalf("read second turn: %v", err)
	}
	fmt.Printf("WS_TURN connection_request_id=%s turn_index=2 response_id=%s\n", *requestID, secondID)
	_ = client.writeClose()
}

func dial(endpoint, token, requestID string) (*wsClient, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme != "ws" || parsed.Host == "" {
		return nil, fmt.Errorf("only ws:// URLs with a host are supported")
	}
	address := parsed.Host
	if !strings.Contains(address, ":") {
		address += ":80"
	}
	connection, err := net.DialTimeout("tcp", address, 5*time.Second)
	if err != nil {
		return nil, err
	}
	reader := bufio.NewReader(connection)
	writer := bufio.NewWriter(connection)
	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		_ = connection.Close()
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)
	path := parsed.EscapedPath()
	if path == "" {
		path = "/"
	}
	if parsed.RawQuery != "" {
		path += "?" + parsed.RawQuery
	}
	request := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\nX-Client-Request-ID: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n", path, parsed.Host, token, requestID, key)
	if _, err := writer.WriteString(request); err != nil {
		_ = connection.Close()
		return nil, err
	}
	if err := writer.Flush(); err != nil {
		_ = connection.Close()
		return nil, err
	}
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		_ = connection.Close()
		return nil, err
	}
	if response.StatusCode != http.StatusSwitchingProtocols {
		_ = response.Body.Close()
		_ = connection.Close()
		return nil, fmt.Errorf("upgrade status=%s", response.Status)
	}
	expectedHash := sha1.Sum([]byte(key + websocketMagic))
	expectedAccept := base64.StdEncoding.EncodeToString(expectedHash[:])
	if response.Header.Get("Sec-WebSocket-Accept") != expectedAccept {
		_ = connection.Close()
		return nil, fmt.Errorf("server returned an invalid websocket accept key")
	}
	return &wsClient{connection: connection, reader: reader, writer: writer}, nil
}

func (client *wsClient) writeText(payload string) error {
	return client.writeFrame(0x1, []byte(payload))
}

func (client *wsClient) writeClose() error {
	return client.writeFrame(0x8, nil)
}

func (client *wsClient) writeFrame(opcode byte, payload []byte) error {
	if client == nil || client.writer == nil {
		return fmt.Errorf("client is not connected")
	}
	if err := client.writer.WriteByte(0x80 | opcode); err != nil {
		return err
	}
	length := len(payload)
	switch {
	case length <= 125:
		if err := client.writer.WriteByte(0x80 | byte(length)); err != nil {
			return err
		}
	case length <= 65535:
		if err := client.writer.WriteByte(0x80 | 126); err != nil {
			return err
		}
		if err := binary.Write(client.writer, binary.BigEndian, uint16(length)); err != nil {
			return err
		}
	default:
		if err := client.writer.WriteByte(0x80 | 127); err != nil {
			return err
		}
		if err := binary.Write(client.writer, binary.BigEndian, uint64(length)); err != nil {
			return err
		}
	}
	mask := make([]byte, 4)
	if _, err := rand.Read(mask); err != nil {
		return err
	}
	if _, err := client.writer.Write(mask); err != nil {
		return err
	}
	for index := range payload {
		payload[index] ^= mask[index%len(mask)]
	}
	if _, err := client.writer.Write(payload); err != nil {
		return err
	}
	return client.writer.Flush()
}

func (client *wsClient) readThroughTerminal(turn int, disconnectAfterPartial bool) (string, error) {
	if client == nil || client.connection == nil {
		return "", fmt.Errorf("client is not connected")
	}
	deadline := time.Now().Add(15 * time.Second)
	for events := 0; events < 16; events++ {
		if err := client.connection.SetReadDeadline(deadline); err != nil {
			return "", err
		}
		opcode, payload, err := readFrame(client.reader)
		if err != nil {
			return "", err
		}
		if opcode == 0x8 {
			return "", fmt.Errorf("server closed before turn %d completed", turn)
		}
		if opcode == 0x9 {
			if err := client.writeFrame(0xA, payload); err != nil {
				return "", err
			}
			continue
		}
		if opcode != 0x1 {
			continue
		}
		var event responseEnvelope
		if err := json.Unmarshal(payload, &event); err != nil {
			return "", fmt.Errorf("invalid server JSON: %w", err)
		}
		if disconnectAfterPartial && event.Type == "response.output_text.delta" && strings.Contains(event.Delta, "e2e ws disconnect partial") {
			return event.ResponseID, closeWithRST(client.connection)
		}
		if event.Type == "response.completed" {
			if event.Response.ID == "" {
				return "", fmt.Errorf("turn %d completed without response id", turn)
			}
			return event.Response.ID, nil
		}
	}
	return "", fmt.Errorf("turn %d exceeded deterministic fixture event limit", turn)
}

func readFrame(reader *bufio.Reader) (byte, []byte, error) {
	first, err := reader.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	second, err := reader.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	if second&0x80 != 0 {
		return 0, nil, fmt.Errorf("server unexpectedly masked a websocket frame")
	}
	length := uint64(second & 0x7f)
	switch length {
	case 126:
		var value uint16
		if err := binary.Read(reader, binary.BigEndian, &value); err != nil {
			return 0, nil, err
		}
		length = uint64(value)
	case 127:
		if err := binary.Read(reader, binary.BigEndian, &length); err != nil {
			return 0, nil, err
		}
	}
	if length > 1<<20 {
		return 0, nil, fmt.Errorf("server websocket frame exceeds client limit: %d", length)
	}
	payload := make([]byte, int(length))
	if _, err := io.ReadFull(reader, payload); err != nil {
		return 0, nil, err
	}
	return first & 0x0f, payload, nil
}

func closeWithRST(conn net.Conn) error {
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetLinger(0)
	}
	return conn.Close()
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "responses-ws-client: "+format+"\n", args...)
	os.Exit(1)
}
