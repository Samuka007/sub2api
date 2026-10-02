package admin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type oneClickAccountNotesAdminServiceStub struct {
	service.AdminService
	mu sync.Mutex

	previewResult *service.OneClickAccountNotesPreview
	applyResult   *service.OneClickAccountNotesApplyResult
	previewErr    error
	applyErr      error
	previewCalls  int
	applyCalls    int
	previewBodies [][]byte
	applyBodies   [][]byte
	applyDigests  []string
	applyContexts []any
}

type blockingOneClickAccountNotesAdminServiceStub struct {
	service.AdminService

	previewEntered chan struct{}
	releasePreview chan struct{}
	applyCalled    chan struct{}
}

// observingOneClickAccountNotesIdempotencyRepo keeps the handler test tied to
// the transactional coordinator contract instead of only its response shape.
type observingOneClickAccountNotesIdempotencyRepo struct {
	*memoryIdempotencyRepoStub
	withinTransactionCalls atomic.Int32
	transactionMarker      any
}

type oneClickAccountNotesTransactionContextKey struct{}

func (r *observingOneClickAccountNotesIdempotencyRepo) WithinTransaction(
	ctx context.Context,
	execute func(context.Context, service.IdempotencyRepository) error,
) error {
	r.withinTransactionCalls.Add(1)
	if r.transactionMarker != nil {
		ctx = context.WithValue(ctx, oneClickAccountNotesTransactionContextKey{}, r.transactionMarker)
	}
	return r.memoryIdempotencyRepoStub.WithinTransaction(ctx, execute)
}

func (s *blockingOneClickAccountNotesAdminServiceStub) PreviewOneClickAccountNotes(
	ctx context.Context,
	_ []byte,
) (*service.OneClickAccountNotesPreview, error) {
	select {
	case s.previewEntered <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case <-s.releasePreview:
		return &service.OneClickAccountNotesPreview{}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *blockingOneClickAccountNotesAdminServiceStub) ApplyOneClickAccountNotes(
	_ context.Context,
	_ []byte,
	_ string,
) (*service.OneClickAccountNotesApplyResult, error) {
	s.applyCalled <- struct{}{}
	return &service.OneClickAccountNotesApplyResult{}, nil
}

func (s *oneClickAccountNotesAdminServiceStub) PreviewOneClickAccountNotes(_ context.Context, content []byte) (*service.OneClickAccountNotesPreview, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.previewCalls++
	s.previewBodies = append(s.previewBodies, append([]byte(nil), content...))
	if s.previewResult == nil {
		s.previewResult = &service.OneClickAccountNotesPreview{}
	}
	return s.previewResult, s.previewErr
}

func (s *oneClickAccountNotesAdminServiceStub) ApplyOneClickAccountNotes(ctx context.Context, content []byte, previewDigest string) (*service.OneClickAccountNotesApplyResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applyCalls++
	s.applyBodies = append(s.applyBodies, append([]byte(nil), content...))
	s.applyDigests = append(s.applyDigests, previewDigest)
	s.applyContexts = append(s.applyContexts, ctx.Value(oneClickAccountNotesTransactionContextKey{}))
	if s.applyResult == nil {
		s.applyResult = &service.OneClickAccountNotesApplyResult{}
	}
	return s.applyResult, s.applyErr
}

type oneClickAccountNotesMultipartPart struct {
	name     string
	filename string
	value    []byte
}

type gatedOneClickAccountNotesBody struct {
	data    []byte
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *gatedOneClickAccountNotesBody) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.started) })
	<-r.release
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

func (*gatedOneClickAccountNotesBody) Close() error { return nil }

type timeoutOneClickAccountNotesBody struct {
	started   chan struct{}
	closed    chan struct{}
	startOnce sync.Once
	closeOnce sync.Once
}

func (r *timeoutOneClickAccountNotesBody) Read([]byte) (int, error) {
	r.startOnce.Do(func() { close(r.started) })
	<-r.closed
	return 0, io.ErrClosedPipe
}

func (r *timeoutOneClickAccountNotesBody) Close() error {
	r.closeOnce.Do(func() { close(r.closed) })
	return nil
}

func buildOneClickAccountNotesMultipart(t *testing.T, parts ...oneClickAccountNotesMultipartPart) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, item := range parts {
		if item.filename != "" {
			part, err := writer.CreateFormFile(item.name, item.filename)
			require.NoError(t, err)
			_, err = part.Write(item.value)
			require.NoError(t, err)
			continue
		}
		require.NoError(t, writer.WriteField(item.name, string(item.value)))
	}
	require.NoError(t, writer.Close())
	return &body, writer.FormDataContentType()
}

func setupOneClickAccountNotesHandler(serviceStub service.AdminService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	handler := newOneClickAccountNotesTestHandler(serviceStub)
	router := gin.New()
	router.POST("/preview", handler.PreviewOneClickAccountNotes)
	router.POST("/apply", handler.ApplyOneClickAccountNotes)
	return router
}

func newOneClickAccountNotesTestHandler(serviceStub service.AdminService) *AccountHandler {
	return NewAccountHandler(serviceStub, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
}

func performOneClickAccountNotesRequest(
	t *testing.T,
	router http.Handler,
	path string,
	parts []oneClickAccountNotesMultipartPart,
	idempotencyKey string,
) *httptest.ResponseRecorder {
	t.Helper()
	body, contentType := buildOneClickAccountNotesMultipart(t, parts...)
	request := httptest.NewRequest(http.MethodPost, path, body)
	request.Header.Set("Content-Type", contentType)
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func oneClickAccountNotesResponseReason(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var envelope response.Response
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	return envelope.Reason
}

func TestPreviewOneClickAccountNotesAcceptsOneTxtFile(t *testing.T) {
	service.SetDefaultIdempotencyCoordinator(nil)
	content := []byte("user@example.com---https://mail.example.test/inbox?token=canary\n")
	serviceStub := &oneClickAccountNotesAdminServiceStub{}
	router := setupOneClickAccountNotesHandler(serviceStub)

	recorder := performOneClickAccountNotesRequest(t, router, "/preview", []oneClickAccountNotesMultipartPart{
		{name: "file", filename: "notes.TXT", value: content},
	}, "")

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))
	require.Equal(t, 1, serviceStub.previewCalls)
	require.Equal(t, content, serviceStub.previewBodies[0])
}

func TestPreviewOneClickAccountNotesSuccessJSONContract(t *testing.T) {
	serviceStub := &oneClickAccountNotesAdminServiceStub{previewResult: &service.OneClickAccountNotesPreview{
		PreviewDigest:      "preview-contract-digest",
		TotalLines:         8,
		ValidLines:         6,
		InvalidLines:       2,
		DuplicateLines:     1,
		ConflictLines:      1,
		MatchedLines:       4,
		UnmatchedLines:     1,
		MatchedAccounts:    5,
		WillUpdateAccounts: 3,
		UnchangedAccounts:  2,
		CanApply:           true,
		Entries: []service.OneClickAccountNotesEntry{{
			LineNumber:         7,
			Email:              "contract@example.com",
			Status:             "contract-status",
			MatchedAccounts:    2,
			WillUpdateAccounts: 1,
			DuplicateOfLine:    3,
			Reason:             "contract-reason",
		}},
	}}
	router := setupOneClickAccountNotesHandler(serviceStub)

	recorder := performOneClickAccountNotesRequest(t, router, "/preview", []oneClickAccountNotesMultipartPart{{
		name: "file", filename: "notes.txt", value: []byte("contract@example.com---contract-note"),
	}}, "")

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "application/json; charset=utf-8", recorder.Header().Get("Content-Type"))
	require.JSONEq(t, `{
		"code": 0,
		"message": "success",
		"data": {
			"preview_digest": "preview-contract-digest",
			"total_lines": 8,
			"valid_lines": 6,
			"invalid_lines": 2,
			"duplicate_lines": 1,
			"conflict_lines": 1,
			"matched_lines": 4,
			"unmatched_lines": 1,
			"matched_accounts": 5,
			"will_update_accounts": 3,
			"unchanged_accounts": 2,
			"can_apply": true,
			"entries": [{
				"line_number": 7,
				"email": "contract@example.com",
				"status": "contract-status",
				"matched_accounts": 2,
				"will_update_accounts": 1,
				"duplicate_of_line": 3,
				"reason": "contract-reason"
			}]
		}
	}`, recorder.Body.String())
}

func TestPreviewOneClickAccountNotesRejectsWhenConcurrencySlotsAreFull(t *testing.T) {
	serviceStub := &oneClickAccountNotesAdminServiceStub{}
	handler := newOneClickAccountNotesTestHandler(serviceStub)
	for range oneClickAccountNotesMaxConcurrentRequests {
		handler.oneClickAccountNotesSlots <- struct{}{}
	}
	t.Cleanup(func() {
		for range oneClickAccountNotesMaxConcurrentRequests {
			<-handler.oneClickAccountNotesSlots
		}
	})
	router := gin.New()
	router.POST("/preview", handler.PreviewOneClickAccountNotes)

	recorder := performOneClickAccountNotesRequest(t, router, "/preview", []oneClickAccountNotesMultipartPart{{
		name: "file", filename: "notes.txt", value: []byte("owner@example.com---note"),
	}}, "")

	require.Equal(t, http.StatusTooManyRequests, recorder.Code)
	require.Equal(t, "ACCOUNT_NOTE_IMPORT_BUSY", oneClickAccountNotesResponseReason(t, recorder))
	require.Equal(t, "1", recorder.Header().Get("Retry-After"))
	require.Zero(t, serviceStub.previewCalls)
}

func TestPreviewOneClickAccountNotesBoundsConcurrentBodyReads(t *testing.T) {
	service.SetDefaultIdempotencyCoordinator(nil)
	serviceStub := &oneClickAccountNotesAdminServiceStub{}
	handler := newOneClickAccountNotesTestHandler(serviceStub)
	router := gin.New()
	router.POST("/preview", handler.PreviewOneClickAccountNotes)

	type slowUpload struct {
		body     *gatedOneClickAccountNotesBody
		recorder *httptest.ResponseRecorder
		done     chan struct{}
	}
	uploads := make([]slowUpload, 0, oneClickAccountNotesMaxConcurrentRequests)
	for index := range oneClickAccountNotesMaxConcurrentRequests {
		body, contentType := buildOneClickAccountNotesMultipart(t, oneClickAccountNotesMultipartPart{
			name: "file", filename: "notes.txt", value: []byte(fmt.Sprintf("slow-%d@example.com---note", index)),
		})
		gatedBody := &gatedOneClickAccountNotesBody{
			data:    body.Bytes(),
			started: make(chan struct{}),
			release: make(chan struct{}),
		}
		request := httptest.NewRequest(http.MethodPost, "/preview", gatedBody)
		request.Header.Set("Content-Type", contentType)
		upload := slowUpload{body: gatedBody, recorder: httptest.NewRecorder(), done: make(chan struct{})}
		uploads = append(uploads, upload)
		go func() {
			router.ServeHTTP(upload.recorder, request)
			close(upload.done)
		}()
	}

	for _, upload := range uploads {
		select {
		case <-upload.body.started:
		case <-time.After(time.Second):
			t.Fatal("slow request did not start reading its body")
		}
	}

	busy := performOneClickAccountNotesRequest(t, router, "/preview", []oneClickAccountNotesMultipartPart{{
		name: "file", filename: "notes.txt", value: []byte("fast@example.com---note"),
	}}, "")
	require.Equal(t, http.StatusTooManyRequests, busy.Code)
	require.Equal(t, "ACCOUNT_NOTE_IMPORT_BUSY", oneClickAccountNotesResponseReason(t, busy))

	for _, upload := range uploads {
		close(upload.body.release)
	}
	for _, upload := range uploads {
		select {
		case <-upload.done:
		case <-time.After(time.Second):
			t.Fatal("slow request did not finish after body release")
		}
		require.Equal(t, http.StatusOK, upload.recorder.Code)
	}
}

func TestReadOneClickAccountNotesMultipartTimesOutSlowBody(t *testing.T) {
	body := &timeoutOneClickAccountNotesBody{
		started: make(chan struct{}),
		closed:  make(chan struct{}),
	}
	request := httptest.NewRequest(http.MethodPost, "/preview", body)
	request.Header.Set("Content-Type", "multipart/form-data; boundary=slow-upload")
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = request

	startedAt := time.Now()
	_, err := readOneClickAccountNotesMultipartWithin(c, false, 25*time.Millisecond)
	require.Error(t, err)
	require.Equal(t, http.StatusRequestTimeout, infraerrors.Code(err))
	require.Equal(t, "ACCOUNT_NOTE_IMPORT_UPLOAD_TIMEOUT", infraerrors.Reason(err))
	require.Less(t, time.Since(startedAt), time.Second)
	select {
	case <-body.started:
	default:
		t.Fatal("request body was not read")
	}
}

func TestPreviewOneClickAccountNotesTransportDeadlineStopsSlowChunkedUpload(t *testing.T) {
	serviceStub := &oneClickAccountNotesAdminServiceStub{}
	handler := newOneClickAccountNotesTestHandler(serviceStub)
	handler.oneClickAccountNotesUploadTimeout = 75 * time.Millisecond
	router := gin.New()
	router.POST("/preview", handler.PreviewOneClickAccountNotes)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	conn, reader := openSlowOneClickAccountNotesRequest(t, server.URL, "/preview", "")
	defer func() { _ = conn.Close() }()
	startedAt := time.Now()
	responseEnvelope := readOneClickAccountNotesHTTPResponse(t, conn, reader)

	require.Equal(t, http.StatusRequestTimeout, responseEnvelope.status)
	require.Equal(t, "ACCOUNT_NOTE_IMPORT_UPLOAD_TIMEOUT", responseEnvelope.reason)
	require.True(t, responseEnvelope.close)
	require.Less(t, time.Since(startedAt), 2*time.Second)
	require.Zero(t, serviceStub.previewCalls)
}

func TestPreviewOneClickAccountNotesTransportDeadlineWaitsForChunkedTerminator(t *testing.T) {
	serviceStub := &oneClickAccountNotesAdminServiceStub{}
	handler := newOneClickAccountNotesTestHandler(serviceStub)
	handler.oneClickAccountNotesUploadTimeout = 75 * time.Millisecond
	router := gin.New()
	router.POST("/preview", handler.PreviewOneClickAccountNotes)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	completeMultipart := "--slow-upload\r\nContent-Disposition: form-data; name=\"file\"; filename=\"notes.txt\"\r\n\r\nowner@example.com---note\r\n--slow-upload--\r\n"
	conn, reader := openOneClickAccountNotesChunkedRequest(t, server.URL, "/preview", "", completeMultipart)
	defer func() { _ = conn.Close() }()
	startedAt := time.Now()
	responseEnvelope := readOneClickAccountNotesHTTPResponse(t, conn, reader)

	require.Equal(t, http.StatusRequestTimeout, responseEnvelope.status)
	require.Equal(t, "ACCOUNT_NOTE_IMPORT_UPLOAD_TIMEOUT", responseEnvelope.reason)
	require.True(t, responseEnvelope.close)
	require.Less(t, time.Since(startedAt), 2*time.Second)
	require.Zero(t, serviceStub.previewCalls)
}

func TestPreviewOneClickAccountNotesBusyRejectsUnreadChunkedBodyPromptly(t *testing.T) {
	serviceStub := &oneClickAccountNotesAdminServiceStub{}
	handler := newOneClickAccountNotesTestHandler(serviceStub)
	handler.oneClickAccountNotesUploadTimeout = 5 * time.Second
	for range oneClickAccountNotesMaxConcurrentRequests {
		handler.oneClickAccountNotesSlots <- struct{}{}
	}
	t.Cleanup(func() {
		for range oneClickAccountNotesMaxConcurrentRequests {
			<-handler.oneClickAccountNotesSlots
		}
	})
	router := gin.New()
	router.POST("/preview", handler.PreviewOneClickAccountNotes)
	serverClosed := make(chan struct{}, 1)
	server := httptest.NewUnstartedServer(router)
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateClosed {
			select {
			case serverClosed <- struct{}{}:
			default:
			}
		}
	}
	server.Start()
	t.Cleanup(server.Close)

	conn, reader := openSlowOneClickAccountNotesRequest(t, server.URL, "/preview", "")
	defer func() { _ = conn.Close() }()
	startedAt := time.Now()
	responseEnvelope := readOneClickAccountNotesHTTPResponse(t, conn, reader)

	require.Equal(t, http.StatusTooManyRequests, responseEnvelope.status)
	require.Equal(t, "ACCOUNT_NOTE_IMPORT_BUSY", responseEnvelope.reason)
	require.True(t, responseEnvelope.close)
	require.Less(t, time.Since(startedAt), time.Second)
	require.Zero(t, serviceStub.previewCalls)
	select {
	case <-serverClosed:
	case <-time.After(time.Second):
		t.Fatal("server did not close the rejected upload connection promptly")
	}
}

type oneClickAccountNotesHTTPResponse struct {
	status int
	reason string
	close  bool
}

func openSlowOneClickAccountNotesRequest(
	t *testing.T,
	serverURL string,
	path string,
	idempotencyKey string,
) (net.Conn, *bufio.Reader) {
	t.Helper()
	partialBody := "--slow-upload\r\nContent-Disposition: form-data; name=\"file\"; filename=\"notes.txt\"\r\n\r\nowner@example.com---note"
	return openOneClickAccountNotesChunkedRequest(t, serverURL, path, idempotencyKey, partialBody)
}

func openOneClickAccountNotesChunkedRequest(
	t *testing.T,
	serverURL string,
	path string,
	idempotencyKey string,
	chunkBody string,
) (net.Conn, *bufio.Reader) {
	t.Helper()
	address := strings.TrimPrefix(serverURL, "http://")
	conn, err := net.DialTimeout("tcp", address, time.Second)
	require.NoError(t, err)
	reader := bufio.NewReader(conn)
	_, err = fmt.Fprintf(
		conn,
		"POST %s HTTP/1.1\r\nHost: %s\r\nTransfer-Encoding: chunked\r\nContent-Type: multipart/form-data; boundary=slow-upload\r\n%s\r\n",
		path,
		address,
		idempotencyKey,
	)
	require.NoError(t, err)
	_, err = fmt.Fprintf(conn, "%x\r\n%s\r\n", len(chunkBody), chunkBody)
	require.NoError(t, err)
	return conn, reader
}

func readOneClickAccountNotesHTTPResponse(
	t *testing.T,
	conn net.Conn,
	reader *bufio.Reader,
) oneClickAccountNotesHTTPResponse {
	t.Helper()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	result, err := http.ReadResponse(reader, &http.Request{Method: http.MethodPost})
	require.NoError(t, err)
	defer func() { _ = result.Body.Close() }()
	encoded, err := io.ReadAll(result.Body)
	require.NoError(t, err)
	var envelope response.Response
	require.NoError(t, json.Unmarshal(encoded, &envelope))
	return oneClickAccountNotesHTTPResponse{status: result.StatusCode, reason: envelope.Reason, close: result.Close}
}

func TestApplyOneClickAccountNotesSharesConcurrencySlotsWithPreview(t *testing.T) {
	service.SetDefaultIdempotencyCoordinator(nil)
	serviceStub := &blockingOneClickAccountNotesAdminServiceStub{
		previewEntered: make(chan struct{}, oneClickAccountNotesMaxConcurrentRequests),
		releasePreview: make(chan struct{}),
		applyCalled:    make(chan struct{}, 1),
	}
	handler := newOneClickAccountNotesTestHandler(serviceStub)
	router := gin.New()
	router.POST("/preview", handler.PreviewOneClickAccountNotes)
	router.POST("/apply", handler.ApplyOneClickAccountNotes)

	var previewRequests sync.WaitGroup
	previewRecorders := make([]*httptest.ResponseRecorder, 0, oneClickAccountNotesMaxConcurrentRequests)
	var releaseOnce sync.Once
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(serviceStub.releasePreview) })
		previewRequests.Wait()
	})
	for range oneClickAccountNotesMaxConcurrentRequests {
		body, contentType := buildOneClickAccountNotesMultipart(t, oneClickAccountNotesMultipartPart{
			name: "file", filename: "notes.txt", value: []byte("preview@example.com---note"),
		})
		request := httptest.NewRequest(http.MethodPost, "/preview", body)
		request.Header.Set("Content-Type", contentType)
		recorder := httptest.NewRecorder()
		previewRecorders = append(previewRecorders, recorder)
		previewRequests.Add(1)
		go func() {
			defer previewRequests.Done()
			router.ServeHTTP(recorder, request)
		}()
	}

	for range oneClickAccountNotesMaxConcurrentRequests {
		select {
		case <-serviceStub.previewEntered:
		case <-time.After(time.Second):
			t.Fatal("preview request did not occupy a shared concurrency slot")
		}
	}

	applyRecorder := performOneClickAccountNotesRequest(t, router, "/apply", []oneClickAccountNotesMultipartPart{
		{name: "file", filename: "notes.txt", value: []byte("apply@example.com---note")},
		{name: "preview_digest", value: []byte("digest-1")},
	}, "apply-key")
	releaseOnce.Do(func() { close(serviceStub.releasePreview) })
	previewRequests.Wait()

	require.Equal(t, http.StatusTooManyRequests, applyRecorder.Code)
	require.Equal(t, "ACCOUNT_NOTE_IMPORT_BUSY", oneClickAccountNotesResponseReason(t, applyRecorder))
	require.Equal(t, "1", applyRecorder.Header().Get("Retry-After"))
	require.Empty(t, serviceStub.applyCalled)
	for _, recorder := range previewRecorders {
		require.Equal(t, http.StatusOK, recorder.Code)
	}
}

func TestPreviewOneClickAccountNotesAcceptsFileAtHardLimit(t *testing.T) {
	content := bytes.Repeat([]byte{'a'}, oneClickAccountNotesMaxFileBytes)
	serviceStub := &oneClickAccountNotesAdminServiceStub{}
	router := setupOneClickAccountNotesHandler(serviceStub)

	recorder := performOneClickAccountNotesRequest(t, router, "/preview", []oneClickAccountNotesMultipartPart{
		{name: "file", filename: "notes.txt", value: content},
	}, "")

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, 1, serviceStub.previewCalls)
	require.Len(t, serviceStub.previewBodies[0], oneClickAccountNotesMaxFileBytes)
}

func TestPreviewOneClickAccountNotesPreservesRawMultipartFileBytes(t *testing.T) {
	serviceStub := &oneClickAccountNotesAdminServiceStub{}
	router := setupOneClickAccountNotesHandler(serviceStub)
	const boundary = "one-click-notes-raw-boundary"
	content := []byte("user@example.com---https://mail.example.test/inbox?token=abc=3Ddef")
	var body bytes.Buffer
	_, err := body.WriteString("--" + boundary + "\r\n" +
		"Content-Disposition: form-data; name=\"file\"; filename=\"notes.txt\"\r\n" +
		"Content-Type: text/plain\r\n" +
		"Content-Transfer-Encoding: quoted-printable\r\n\r\n")
	require.NoError(t, err)
	_, err = body.Write(content)
	require.NoError(t, err)
	_, err = body.WriteString("\r\n--" + boundary + "--\r\n")
	require.NoError(t, err)

	request := httptest.NewRequest(http.MethodPost, "/preview", &body)
	request.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, 1, serviceStub.previewCalls)
	require.Equal(t, content, serviceStub.previewBodies[0])
}

func TestPreviewOneClickAccountNotesRejectsInvalidMultipart(t *testing.T) {
	tests := []struct {
		name       string
		parts      []oneClickAccountNotesMultipartPart
		wantStatus int
		wantReason string
	}{
		{
			name:       "missing file",
			wantStatus: http.StatusBadRequest,
			wantReason: "ACCOUNT_NOTE_IMPORT_FILE_REQUIRED",
		},
		{
			name: "empty file",
			parts: []oneClickAccountNotesMultipartPart{
				{name: "file", filename: "notes.txt"},
			},
			wantStatus: http.StatusBadRequest,
			wantReason: "ACCOUNT_NOTE_IMPORT_FILE_EMPTY",
		},
		{
			name: "wrong extension",
			parts: []oneClickAccountNotesMultipartPart{
				{name: "file", filename: "notes.csv", value: []byte("line")},
			},
			wantStatus: http.StatusBadRequest,
			wantReason: "ACCOUNT_NOTE_IMPORT_FILE_TYPE_INVALID",
		},
		{
			name: "multiple files",
			parts: []oneClickAccountNotesMultipartPart{
				{name: "file", filename: "one.txt", value: []byte("one")},
				{name: "file", filename: "two.txt", value: []byte("two")},
			},
			wantStatus: http.StatusBadRequest,
			wantReason: "ACCOUNT_NOTE_IMPORT_FILE_INVALID",
		},
		{
			name: "unexpected field",
			parts: []oneClickAccountNotesMultipartPart{
				{name: "file", filename: "notes.txt", value: []byte("line")},
				{name: "extra", value: []byte("not allowed")},
			},
			wantStatus: http.StatusBadRequest,
			wantReason: "ACCOUNT_NOTE_IMPORT_MULTIPART_INVALID",
		},
		{
			name: "file over limit",
			parts: []oneClickAccountNotesMultipartPart{
				{name: "file", filename: "notes.txt", value: bytes.Repeat([]byte{'a'}, oneClickAccountNotesMaxFileBytes+1)},
			},
			wantStatus: http.StatusRequestEntityTooLarge,
			wantReason: "ACCOUNT_NOTE_IMPORT_FILE_TOO_LARGE",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			serviceStub := &oneClickAccountNotesAdminServiceStub{}
			router := setupOneClickAccountNotesHandler(serviceStub)
			recorder := performOneClickAccountNotesRequest(t, router, "/preview", test.parts, "")

			require.Equal(t, test.wantStatus, recorder.Code)
			require.Equal(t, test.wantReason, oneClickAccountNotesResponseReason(t, recorder))
			require.Zero(t, serviceStub.previewCalls)
		})
	}
}

func TestPreviewOneClickAccountNotesRejectsNonMultipartRequest(t *testing.T) {
	serviceStub := &oneClickAccountNotesAdminServiceStub{}
	router := setupOneClickAccountNotesHandler(serviceStub)
	request := httptest.NewRequest(http.MethodPost, "/preview", strings.NewReader("not multipart"))
	request.Header.Set("Content-Type", "text/plain")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Equal(t, "ACCOUNT_NOTE_IMPORT_MULTIPART_REQUIRED", oneClickAccountNotesResponseReason(t, recorder))
	require.Zero(t, serviceStub.previewCalls)
}

func TestPreviewOneClickAccountNotesRejectsMalformedHeaderWithoutEchoingCanary(t *testing.T) {
	serviceStub := &oneClickAccountNotesAdminServiceStub{}
	router := setupOneClickAccountNotesHandler(serviceStub)
	const boundary = "one-click-notes-boundary"
	const canary = "audit-canary-secret"
	body := "--" + boundary + "\r\n" + canary + "\r\n\r\ncontent\r\n--" + boundary + "--\r\n"
	request := httptest.NewRequest(http.MethodPost, "/preview", strings.NewReader(body))
	request.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Equal(t, "ACCOUNT_NOTE_IMPORT_MULTIPART_INVALID", oneClickAccountNotesResponseReason(t, recorder))
	require.NotContains(t, recorder.Body.String(), canary)
	require.Zero(t, serviceStub.previewCalls)
}

func TestPreviewOneClickAccountNotesRejectsOversizedMultipartRequest(t *testing.T) {
	serviceStub := &oneClickAccountNotesAdminServiceStub{}
	router := setupOneClickAccountNotesHandler(serviceStub)
	parts := []oneClickAccountNotesMultipartPart{
		{name: "file", filename: "notes.txt", value: bytes.Repeat([]byte{'a'}, oneClickAccountNotesMaxFileBytes)},
		{name: "extra", value: bytes.Repeat([]byte{'b'}, oneClickAccountNotesMultipartOverheadBytes)},
	}
	recorder := performOneClickAccountNotesRequest(t, router, "/preview", parts, "")

	require.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code)
	require.Equal(t, "ACCOUNT_NOTE_IMPORT_REQUEST_TOO_LARGE", oneClickAccountNotesResponseReason(t, recorder))
	require.Zero(t, serviceStub.previewCalls)
}

func TestApplyOneClickAccountNotesRequiresKeyAndPreviewDigest(t *testing.T) {
	service.SetDefaultIdempotencyCoordinator(nil)
	serviceStub := &oneClickAccountNotesAdminServiceStub{}
	router := setupOneClickAccountNotesHandler(serviceStub)
	filePart := oneClickAccountNotesMultipartPart{name: "file", filename: "notes.txt", value: []byte("user@example.com---note")}

	missingKey := performOneClickAccountNotesRequest(t, router, "/apply", []oneClickAccountNotesMultipartPart{
		filePart,
		{name: "preview_digest", value: []byte("digest-1")},
	}, "")
	require.Equal(t, http.StatusBadRequest, missingKey.Code)
	require.Equal(t, "IDEMPOTENCY_KEY_REQUIRED", oneClickAccountNotesResponseReason(t, missingKey))

	missingDigest := performOneClickAccountNotesRequest(t, router, "/apply", []oneClickAccountNotesMultipartPart{filePart}, "apply-key")
	require.Equal(t, http.StatusBadRequest, missingDigest.Code)
	require.Equal(t, "ACCOUNT_NOTE_IMPORT_PREVIEW_DIGEST_REQUIRED", oneClickAccountNotesResponseReason(t, missingDigest))

	duplicateDigest := performOneClickAccountNotesRequest(t, router, "/apply", []oneClickAccountNotesMultipartPart{
		filePart,
		{name: "preview_digest", value: []byte("digest-1")},
		{name: "preview_digest", value: []byte("digest-2")},
	}, "apply-key")
	require.Equal(t, http.StatusBadRequest, duplicateDigest.Code)
	require.Equal(t, "ACCOUNT_NOTE_IMPORT_PREVIEW_DIGEST_INVALID", oneClickAccountNotesResponseReason(t, duplicateDigest))

	invalidKey := performOneClickAccountNotesRequest(t, router, "/apply", []oneClickAccountNotesMultipartPart{
		filePart,
		{name: "preview_digest", value: []byte("digest-1")},
	}, strings.Repeat("k", 129))
	require.Equal(t, http.StatusBadRequest, invalidKey.Code)
	require.Equal(t, "IDEMPOTENCY_KEY_INVALID", oneClickAccountNotesResponseReason(t, invalidKey))
	require.Zero(t, serviceStub.applyCalls)
}

func TestApplyOneClickAccountNotesFailsClosedWithoutTransactionalCoordinator(t *testing.T) {
	service.SetDefaultIdempotencyCoordinator(nil)
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(nil) })
	serviceStub := &oneClickAccountNotesAdminServiceStub{}
	router := setupOneClickAccountNotesHandler(serviceStub)

	recorder := performOneClickAccountNotesRequest(t, router, "/apply", []oneClickAccountNotesMultipartPart{
		{name: "file", filename: "notes.txt", value: []byte("user@example.com---note")},
		{name: "preview_digest", value: []byte("preview-digest")},
	}, "apply-key")

	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	require.Equal(t, "IDEMPOTENCY_STORE_UNAVAILABLE", oneClickAccountNotesResponseReason(t, recorder))
	require.Zero(t, serviceStub.applyCalls)
}

func TestApplyOneClickAccountNotesSuccessJSONContract(t *testing.T) {
	cfg := service.DefaultIdempotencyConfig()
	cfg.ObserveOnly = false
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(newMemoryIdempotencyRepoStub(), cfg))
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(nil) })

	serviceStub := &oneClickAccountNotesAdminServiceStub{applyResult: &service.OneClickAccountNotesApplyResult{
		MatchedLines:      11,
		MatchedAccounts:   12,
		UpdatedAccounts:   13,
		UnchangedAccounts: 14,
		UnmatchedLines:    15,
		InvalidLines:      16,
		ConflictLines:     17,
	}}
	router := setupOneClickAccountNotesHandler(serviceStub)

	recorder := performOneClickAccountNotesRequest(t, router, "/apply", []oneClickAccountNotesMultipartPart{
		{name: "file", filename: "notes.txt", value: []byte("contract@example.com---contract-note")},
		{name: "preview_digest", value: []byte("preview-contract-digest")},
	}, "apply-contract-key")

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "application/json; charset=utf-8", recorder.Header().Get("Content-Type"))
	require.JSONEq(t, `{
		"code": 0,
		"message": "success",
		"data": {
			"matched_lines": 11,
			"matched_accounts": 12,
			"updated_accounts": 13,
			"unchanged_accounts": 14,
			"unmatched_lines": 15,
			"invalid_lines": 16,
			"conflict_lines": 17
		}
	}`, recorder.Body.String())
}

func TestApplyOneClickAccountNotesUsesDigestOnlyIdempotencyPayload(t *testing.T) {
	transactionMarker := &struct{}{}
	repository := &observingOneClickAccountNotesIdempotencyRepo{
		memoryIdempotencyRepoStub: newMemoryIdempotencyRepoStub(),
		transactionMarker:         transactionMarker,
	}
	cfg := service.DefaultIdempotencyConfig()
	cfg.ObserveOnly = false
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(repository, cfg))
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(nil) })

	serviceStub := &oneClickAccountNotesAdminServiceStub{applyResult: &service.OneClickAccountNotesApplyResult{
		MatchedLines:      4,
		MatchedAccounts:   3,
		UpdatedAccounts:   2,
		UnchangedAccounts: 1,
		UnmatchedLines:    5,
		InvalidLines:      6,
		ConflictLines:     7,
	}}
	router := setupOneClickAccountNotesHandler(serviceStub)
	canary := "user@example.com---https://mail.example.test/inbox?password=audit-canary-secret"
	parts := func(content, digest string) []oneClickAccountNotesMultipartPart {
		return []oneClickAccountNotesMultipartPart{
			{name: "file", filename: "notes.txt", value: []byte(content)},
			{name: "preview_digest", value: []byte(digest)},
		}
	}

	first := performOneClickAccountNotesRequest(t, router, "/apply", parts(canary, "preview-digest-1"), "same-apply-key")
	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, int32(1), repository.withinTransactionCalls.Load(), "apply must use the transactional idempotency helper")
	require.Equal(t, 1, serviceStub.applyCalls)
	require.Equal(t, canary, string(serviceStub.applyBodies[0]))
	require.Equal(t, "preview-digest-1", serviceStub.applyDigests[0])
	require.Same(t, transactionMarker, serviceStub.applyContexts[0], "handler must pass the coordinator transaction context to the service")
	require.NotContains(t, first.Body.String(), canary)
	var firstResponse struct {
		Data service.OneClickAccountNotesApplyResult `json:"data"`
	}
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &firstResponse))
	require.Equal(t, *serviceStub.applyResult, firstResponse.Data)

	replay := performOneClickAccountNotesRequest(t, router, "/apply", parts(canary, "preview-digest-1"), "same-apply-key")
	require.Equal(t, http.StatusOK, replay.Code)
	require.Equal(t, "true", replay.Header().Get("X-Idempotency-Replayed"))
	require.Equal(t, int32(1), repository.withinTransactionCalls.Load(), "a replay should not execute another transaction")
	require.Equal(t, 1, serviceStub.applyCalls)

	changedFile := performOneClickAccountNotesRequest(t, router, "/apply", parts(canary+"-changed", "preview-digest-1"), "same-apply-key")
	require.Equal(t, http.StatusConflict, changedFile.Code)
	require.Equal(t, "IDEMPOTENCY_KEY_CONFLICT", oneClickAccountNotesResponseReason(t, changedFile))
	require.Equal(t, int32(1), repository.withinTransactionCalls.Load(), "a fingerprint conflict should not execute a transaction")

	changedDigest := performOneClickAccountNotesRequest(t, router, "/apply", parts(canary, "preview-digest-2"), "same-apply-key")
	require.Equal(t, http.StatusConflict, changedDigest.Code)
	require.Equal(t, "IDEMPOTENCY_KEY_CONFLICT", oneClickAccountNotesResponseReason(t, changedDigest))
	require.Equal(t, int32(1), repository.withinTransactionCalls.Load(), "a fingerprint conflict should not execute a transaction")
	require.Equal(t, 1, serviceStub.applyCalls)

	repository.mu.Lock()
	defer repository.mu.Unlock()
	require.Len(t, repository.data, 1)
	for _, record := range repository.data {
		require.NotContains(t, record.RequestFingerprint, canary)
		if record.ResponseBody != nil {
			require.NotContains(t, *record.ResponseBody, canary)
			require.NotContains(t, *record.ResponseBody, "audit-canary-secret")
		}
	}
}
