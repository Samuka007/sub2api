package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

const (
	oneClickAccountNotesMaxFileBytes           = 1 << 20
	oneClickAccountNotesMultipartOverheadBytes = 64 << 10
	oneClickAccountNotesMaxRequestBytes        = oneClickAccountNotesMaxFileBytes + oneClickAccountNotesMultipartOverheadBytes
	oneClickAccountNotesMaxDigestBytes         = 256
	oneClickAccountNotesMaxConcurrentRequests  = 2
	oneClickAccountNotesUploadTimeout          = 30 * time.Second
	oneClickAccountNotesEarlyCloseGrace        = 250 * time.Millisecond
)

type oneClickAccountNotesMultipartPayload struct {
	content       []byte
	previewDigest string
}

type oneClickAccountNotesIdempotencyPayload struct {
	FileSHA256    string `json:"file_sha256"`
	PreviewDigest string `json:"preview_digest"`
}

type oneClickAccountNotesReadDeadline struct {
	controller *http.ResponseController
	active     bool
}

// PreviewOneClickAccountNotes validates an uploaded note file and returns a
// credential-safe match summary without writing account data.
// POST /api/v1/admin/accounts/one-click-notes/preview
func (h *AccountHandler) PreviewOneClickAccountNotes(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	uploadTimeout := h.oneClickAccountNotesUploadDuration()
	readDeadline := startOneClickAccountNotesReadDeadline(c, uploadTimeout)
	release, ok := h.tryAcquireOneClickAccountNotesSlot()
	if !ok {
		c.Header("Retry-After", "1")
		writeOneClickAccountNotesReadError(c, readDeadline, service.ErrOneClickAccountNotesBusy)
		return
	}
	defer release()

	payload, err := readOneClickAccountNotesMultipartWithin(c, false, uploadTimeout)
	if err != nil {
		writeOneClickAccountNotesReadError(c, readDeadline, err)
		return
	}
	readDeadline.clear()

	result, err := h.adminService.PreviewOneClickAccountNotes(c.Request.Context(), payload.content)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, result)
}

// ApplyOneClickAccountNotes atomically applies a previously previewed note
// file. The idempotency fingerprint contains only one-way digests.
// POST /api/v1/admin/accounts/one-click-notes/apply
func (h *AccountHandler) ApplyOneClickAccountNotes(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	uploadTimeout := h.oneClickAccountNotesUploadDuration()
	readDeadline := startOneClickAccountNotesReadDeadline(c, uploadTimeout)
	idempotencyKey, err := service.NormalizeIdempotencyKey(c.GetHeader("Idempotency-Key"))
	if err != nil {
		writeOneClickAccountNotesReadError(c, readDeadline, err)
		return
	}
	if idempotencyKey == "" {
		writeOneClickAccountNotesReadError(c, readDeadline, service.ErrIdempotencyKeyRequired)
		return
	}
	c.Request.Header.Set("Idempotency-Key", idempotencyKey)

	release, ok := h.tryAcquireOneClickAccountNotesSlot()
	if !ok {
		c.Header("Retry-After", "1")
		writeOneClickAccountNotesReadError(c, readDeadline, service.ErrOneClickAccountNotesBusy)
		return
	}
	defer release()

	payload, err := readOneClickAccountNotesMultipartWithin(c, true, uploadTimeout)
	if err != nil {
		writeOneClickAccountNotesReadError(c, readDeadline, err)
		return
	}
	readDeadline.clear()

	contentDigest := sha256.Sum256(payload.content)
	idempotencyPayload := oneClickAccountNotesIdempotencyPayload{
		FileSHA256:    hex.EncodeToString(contentDigest[:]),
		PreviewDigest: payload.previewDigest,
	}

	executeAdminTransactionalIdempotentJSON(
		c,
		"admin.accounts.one_click_notes.apply",
		idempotencyPayload,
		service.DefaultWriteIdempotencyTTL(),
		func(ctx context.Context) (any, error) {
			return h.adminService.ApplyOneClickAccountNotes(ctx, payload.content, payload.previewDigest)
		},
	)
}

func (h *AccountHandler) tryAcquireOneClickAccountNotesSlot() (func(), bool) {
	if h == nil || h.oneClickAccountNotesSlots == nil {
		return func() {}, true
	}
	select {
	case h.oneClickAccountNotesSlots <- struct{}{}:
		return func() { <-h.oneClickAccountNotesSlots }, true
	default:
		return nil, false
	}
}

func (h *AccountHandler) oneClickAccountNotesUploadDuration() time.Duration {
	if h != nil && h.oneClickAccountNotesUploadTimeout > 0 {
		return h.oneClickAccountNotesUploadTimeout
	}
	return oneClickAccountNotesUploadTimeout
}

func startOneClickAccountNotesReadDeadline(c *gin.Context, timeout time.Duration) oneClickAccountNotesReadDeadline {
	deadline := oneClickAccountNotesReadDeadline{controller: http.NewResponseController(c.Writer)}
	deadline.active = deadline.controller.SetReadDeadline(time.Now().Add(timeout)) == nil
	return deadline
}

func writeOneClickAccountNotesReadError(c *gin.Context, deadline oneClickAccountNotesReadDeadline, err error) {
	c.Header("Connection", "close")
	if deadline.active {
		deadline.clear()
	}
	response.ErrorFrom(c, err)
	if deadline.active {
		_ = deadline.controller.Flush()
		_ = deadline.controller.SetReadDeadline(time.Now().Add(oneClickAccountNotesEarlyCloseGrace))
	}
}

func (d oneClickAccountNotesReadDeadline) clear() {
	if d.active {
		_ = d.controller.SetReadDeadline(time.Time{})
	}
}

func readOneClickAccountNotesMultipartWithin(
	c *gin.Context,
	requirePreviewDigest bool,
	timeout time.Duration,
) (*oneClickAccountNotesMultipartPayload, error) {
	readCtx, cancel := context.WithTimeout(c.Request.Context(), timeout)
	defer cancel()
	body := c.Request.Body
	stopClose := context.AfterFunc(readCtx, func() { _ = body.Close() })
	defer stopClose()

	if c.Request.ContentLength > oneClickAccountNotesMaxRequestBytes {
		return nil, oneClickAccountNotesRequestTooLargeError()
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, body, oneClickAccountNotesMaxRequestBytes)

	mediaType, params, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "multipart/form-data") || strings.TrimSpace(params["boundary"]) == "" {
		return nil, infraerrors.BadRequest("ACCOUNT_NOTE_IMPORT_MULTIPART_REQUIRED", "multipart/form-data request is required")
	}

	reader := multipart.NewReader(c.Request.Body, params["boundary"])
	payload := &oneClickAccountNotesMultipartPayload{}
	fileSeen := false
	digestSeen := false

	for {
		part, nextErr := reader.NextRawPart()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return nil, oneClickAccountNotesMultipartReadErrorWithContext(readCtx, nextErr)
		}

		name := part.FormName()
		filename := part.FileName()
		switch {
		case name == "file":
			if filename == "" || fileSeen {
				_ = part.Close()
				return nil, infraerrors.BadRequest("ACCOUNT_NOTE_IMPORT_FILE_INVALID", "exactly one file is required")
			}
			fileSeen = true
			if !strings.EqualFold(filepath.Ext(filename), ".txt") {
				_ = part.Close()
				return nil, infraerrors.BadRequest("ACCOUNT_NOTE_IMPORT_FILE_TYPE_INVALID", "file must use the .txt extension")
			}

			content, readErr := io.ReadAll(io.LimitReader(part, oneClickAccountNotesMaxFileBytes+1))
			_ = part.Close()
			if readErr != nil {
				return nil, oneClickAccountNotesMultipartReadErrorWithContext(readCtx, readErr)
			}
			if len(content) > oneClickAccountNotesMaxFileBytes {
				return nil, infraerrors.New(http.StatusRequestEntityTooLarge, "ACCOUNT_NOTE_IMPORT_FILE_TOO_LARGE", "file exceeds the 1 MiB limit")
			}
			if len(content) == 0 {
				return nil, infraerrors.BadRequest("ACCOUNT_NOTE_IMPORT_FILE_EMPTY", "file must not be empty")
			}
			payload.content = content

		case requirePreviewDigest && name == "preview_digest":
			if filename != "" || digestSeen {
				_ = part.Close()
				return nil, infraerrors.BadRequest("ACCOUNT_NOTE_IMPORT_PREVIEW_DIGEST_INVALID", "exactly one preview_digest is required")
			}
			digestSeen = true
			digest, readErr := io.ReadAll(io.LimitReader(part, oneClickAccountNotesMaxDigestBytes+1))
			_ = part.Close()
			if readErr != nil {
				return nil, oneClickAccountNotesMultipartReadErrorWithContext(readCtx, readErr)
			}
			if len(digest) > oneClickAccountNotesMaxDigestBytes {
				return nil, infraerrors.BadRequest("ACCOUNT_NOTE_IMPORT_PREVIEW_DIGEST_INVALID", "preview_digest is invalid")
			}
			payload.previewDigest = strings.TrimSpace(string(digest))

		default:
			_ = part.Close()
			return nil, infraerrors.BadRequest("ACCOUNT_NOTE_IMPORT_MULTIPART_INVALID", "unexpected multipart field")
		}
	}
	if _, readErr := io.Copy(io.Discard, c.Request.Body); readErr != nil {
		return nil, oneClickAccountNotesMultipartReadErrorWithContext(readCtx, readErr)
	}
	if errors.Is(readCtx.Err(), context.DeadlineExceeded) {
		return nil, oneClickAccountNotesUploadTimeoutError()
	}

	if !fileSeen {
		return nil, infraerrors.BadRequest("ACCOUNT_NOTE_IMPORT_FILE_REQUIRED", "file is required")
	}
	if requirePreviewDigest && (!digestSeen || payload.previewDigest == "") {
		return nil, infraerrors.BadRequest("ACCOUNT_NOTE_IMPORT_PREVIEW_DIGEST_REQUIRED", "preview_digest is required")
	}
	return payload, nil
}

func oneClickAccountNotesMultipartReadError(err error) error {
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		return oneClickAccountNotesRequestTooLargeError()
	}
	return infraerrors.BadRequest("ACCOUNT_NOTE_IMPORT_MULTIPART_INVALID", "invalid multipart request")
}

func oneClickAccountNotesMultipartReadErrorWithContext(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return oneClickAccountNotesUploadTimeoutError()
	}
	var timeoutErr net.Error
	if errors.As(err, &timeoutErr) && timeoutErr.Timeout() {
		return oneClickAccountNotesUploadTimeoutError()
	}
	return oneClickAccountNotesMultipartReadError(err)
}

func oneClickAccountNotesUploadTimeoutError() error {
	return infraerrors.New(http.StatusRequestTimeout, "ACCOUNT_NOTE_IMPORT_UPLOAD_TIMEOUT", "file upload timed out")
}

func oneClickAccountNotesRequestTooLargeError() error {
	return infraerrors.New(http.StatusRequestEntityTooLarge, "ACCOUNT_NOTE_IMPORT_REQUEST_TOO_LARGE", "request body is too large")
}
