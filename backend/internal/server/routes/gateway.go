package routes

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/modeltrace"
	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// RegisterGatewayRoutes 注册 API 网关路由（Claude/OpenAI/Gemini 兼容）
func RegisterGatewayRoutes(
	r *gin.Engine,
	h *handler.Handlers,
	apiKeyAuth middleware.APIKeyAuthMiddleware,
	apiKeyService *service.APIKeyService,
	subscriptionService *service.SubscriptionService,
	opsService *service.OpsService,
	settingService *service.SettingService,
	cfg *config.Config,
	routeDependencies ...any,
) {
	var modelTrace *modeltrace.Manager
	var compositeResolver *service.CompositeRouteResolver
	for _, dependency := range routeDependencies {
		switch value := dependency.(type) {
		case *modeltrace.Manager:
			modelTrace = value
		case *service.CompositeRouteResolver:
			compositeResolver = value
		}
	}
	bodyLimit := middleware.RequestBodyLimit(cfg.Gateway.MaxBodySize)
	textBodyLimit := middleware.RequestBodyLimit(cfg.Gateway.TextMaxBodySize)
	clientRequestID := middleware.ClientRequestID()
	opsErrorLogger := handler.OpsErrorLoggerMiddleware(opsService)
	endpointNorm := handler.InboundEndpointMiddleware()
	if h != nil && h.OpenAIGateway != nil {
		h.OpenAIGateway.SetModelTraceManager(modelTrace)
	}
	modelTraceCandidate := modelTrace.CandidateMiddleware()
	modelTraceDeferred := modelTrace.DeferredCandidateMiddleware()
	compositeTarget := compositeTargetPlatformMiddleware(compositeResolver)
	compositeGeminiTarget := compositeGeminiTargetPlatformMiddleware(compositeResolver)

	// 未分组 Key 拦截中间件（按协议格式区分错误响应）
	requireGroupAnthropic := middleware.RequireGroupAssignment(settingService, middleware.AnthropicErrorWriter)
	requireGroupGoogle := middleware.RequireGroupAssignment(settingService, middleware.GoogleErrorWriter)

	isOpenAIResponsesCompatibleGatewayPlatform := func(c *gin.Context) bool {
		switch getGroupPlatform(c) {
		case service.PlatformOpenAI, service.PlatformGrok:
			return true
		default:
			return false
		}
	}
	isOpenAIGatewayPlatform := func(c *gin.Context) bool {
		return getGroupPlatform(c) == service.PlatformOpenAI
	}
	countTokensHandler := func(c *gin.Context) {
		switch getGroupPlatform(c) {
		case service.PlatformOpenAI:
			h.OpenAIGateway.CountTokens(c)
		case service.PlatformGrok:
			// Grok token counting is a local estimate and must not create a model Trace.
			h.OpenAIGateway.GrokCountTokens(c)
		case service.PlatformAntigravity:
			// Antigravity rejects this endpoint locally without an upstream model call.
			h.Gateway.CountTokens(c)
		default:
			h.Gateway.CountTokens(c)
		}
	}
	modelsHandler := func(c *gin.Context) {
		if isOpenAIGatewayPlatform(c) && c.Query("client_version") != "" {
			h.OpenAIGateway.CodexModels(c)
			return
		}
		h.Gateway.Models(c)
	}
	imagesHandler := func(c *gin.Context) {
		switch getGroupPlatform(c) {
		case service.PlatformOpenAI:
			h.OpenAIGateway.Images(c)
		case service.PlatformGrok:
			h.OpenAIGateway.GrokImages(c)
		default:
			service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalFeatureGate)
			c.JSON(http.StatusNotFound, gin.H{
				"error": gin.H{
					"type":    "not_found_error",
					"message": "Images API is not supported for this platform",
				},
			})
		}
	}
	videoGenerationHandler := func(c *gin.Context) {
		if getGroupPlatform(c) == service.PlatformGrok {
			h.OpenAIGateway.GrokVideoGeneration(c)
			return
		}
		service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalFeatureGate)
		c.JSON(http.StatusNotFound, gin.H{
			"error": gin.H{
				"type":    "not_found_error",
				"message": "Videos API is not supported for this platform",
			},
		})
	}
	videoStatusHandler := func(c *gin.Context) {
		if platform := getGroupPlatform(c); platform == service.PlatformGrok || platform == service.PlatformComposite {
			h.OpenAIGateway.GrokVideoStatus(c)
			return
		}
		service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalFeatureGate)
		c.JSON(http.StatusNotFound, gin.H{
			"error": gin.H{
				"type":    "not_found_error",
				"message": "Videos API is not supported for this platform",
			},
		})
	}
	videoContentHandler := func(c *gin.Context) {
		if platform := getGroupPlatform(c); platform == service.PlatformGrok || platform == service.PlatformComposite {
			h.OpenAIGateway.GrokVideoContent(c)
			return
		}
		service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalFeatureGate)
		c.JSON(http.StatusNotFound, gin.H{
			"error": gin.H{
				"type":    "not_found_error",
				"message": "Videos API is not supported for this platform",
			},
		})
	}
	videoEditHandler := func(c *gin.Context) {
		if getGroupPlatform(c) == service.PlatformGrok {
			h.OpenAIGateway.GrokVideoEdit(c)
			return
		}
		service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalFeatureGate)
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"type": "not_found_error", "message": "Videos API is not supported for this platform"}})
	}
	videoExtensionHandler := func(c *gin.Context) {
		if getGroupPlatform(c) == service.PlatformGrok {
			h.OpenAIGateway.GrokVideoExtension(c)
			return
		}
		service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalFeatureGate)
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"type": "not_found_error", "message": "Videos API is not supported for this platform"}})
	}
	// Responses wildcard subpaths are appended to an authenticated upstream URL.
	// Reject everything outside the closed allowlist before scheduling or forwarding.
	guardResponsesSubpath := func(next gin.HandlerFunc) gin.HandlerFunc {
		return func(c *gin.Context) {
			if !service.IsForwardableOpenAIResponsesRequestPath(c) {
				service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalPolicyDenied)
				c.AbortWithStatusJSON(http.StatusNotFound, gin.H{
					"error": gin.H{
						"type":    "not_found_error",
						"message": "Unsupported responses subpath",
					},
				})
				return
			}
			next(c)
		}
	}
	messagesHandler := func(c *gin.Context) {
		if isOpenAIResponsesCompatibleGatewayPlatform(c) {
			h.OpenAIGateway.Messages(c)
			return
		}
		h.Gateway.Messages(c)
	}
	responsesHandler := func(c *gin.Context) {
		if isOpenAIResponsesCompatibleGatewayPlatform(c) {
			h.OpenAIGateway.Responses(c)
			return
		}
		h.Gateway.Responses(c)
	}
	responsesWebSocketHandler := func(c *gin.Context) {
		h.OpenAIGateway.ResponsesWebSocket(c)
	}
	chatCompletionsHandler := func(c *gin.Context) {
		if isOpenAIResponsesCompatibleGatewayPlatform(c) {
			h.OpenAIGateway.ChatCompletions(c)
			return
		}
		h.Gateway.ChatCompletions(c)
	}
	embeddingsHandler := func(c *gin.Context) {
		if getGroupPlatform(c) != service.PlatformOpenAI {
			service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalFeatureGate)
			c.JSON(http.StatusNotFound, gin.H{
				"error": gin.H{
					"type":    "not_found_error",
					"message": "Embeddings API is not supported for this platform",
				},
			})
			return
		}
		h.OpenAIGateway.Embeddings(c)
	}

	apiKeyAuthHandler := gin.HandlerFunc(apiKeyAuth)
	googleAPIKeyAuth := middleware.APIKeyAuthWithSubscriptionGoogle(apiKeyService, subscriptionService, cfg)

	// API 网关（Claude/OpenAI 兼容）。模型执行与控制面使用显式、互斥的中间件链。
	// OpsErrorLogger owns a pooled writer, so it must wrap Candidate; Candidate must
	// still wrap API-key auth to observe the identity-resolution hook.
	// Billing historically runs after API-key auth but before the group-assignment guard.
	r.GET("/v1/sub2api/billing", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, apiKeyAuthHandler, h.Gateway.KeyBillingInfo)
	// Upstream media and voice root aliases retain the same auth and routing invariants.
	r.POST("/images/generations", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, imagesHandler)
	r.POST("/images/edits", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, imagesHandler)
	r.POST("/videos", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, videoGenerationHandler)
	r.POST("/videos/generations", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, videoGenerationHandler)
	r.POST("/videos/edits", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, videoEditHandler)
	r.POST("/videos/extensions", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, videoExtensionHandler)
	r.GET("/videos/generations/:request_id/content", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, videoContentHandler)
	r.GET("/videos/edits/:request_id/content", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, videoContentHandler)
	r.GET("/videos/extensions/:request_id/content", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, videoContentHandler)
	r.GET("/videos/generations/:request_id", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, videoStatusHandler)
	r.GET("/videos/edits/:request_id", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, videoStatusHandler)
	r.GET("/videos/extensions/:request_id", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, videoStatusHandler)
	r.GET("/videos/:request_id", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, videoStatusHandler)
	r.GET("/videos/:request_id/content", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, videoContentHandler)
	r.POST("/tts", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, func(c *gin.Context) { if getGroupPlatform(c) == service.PlatformGrok { h.OpenAIGateway.GrokVoice(c, "tts"); return }; c.Status(http.StatusNotFound) })
	r.POST("/stt", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, func(c *gin.Context) { if getGroupPlatform(c) == service.PlatformGrok { h.OpenAIGateway.GrokVoice(c, "stt"); return }; c.Status(http.StatusNotFound) })
	r.POST("/web_search", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, func(c *gin.Context) { if getGroupPlatform(c) == service.PlatformGrok { h.Gateway.WebSearch(c); return }; c.Status(http.StatusNotFound) })

	{
		gateway := r.Group("/v1", clientRequestID, opsErrorLogger, modelTraceCandidate)

		// Model execution candidates.
		gateway.POST("/messages", bodyLimit, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, messagesHandler)
		gateway.POST("/live", bodyLimit, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, h.OpenAIGateway.Live)
		gateway.POST("/responses", bodyLimit, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, responsesHandler)
		gateway.POST("/responses/*subpath", bodyLimit, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, guardResponsesSubpath(responsesHandler))
		gateway.POST("/alpha/search", bodyLimit, textBodyLimit, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, h.OpenAIGateway.AlphaSearch)
		gateway.POST("/chat/completions", bodyLimit, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, chatCompletionsHandler)
		gateway.POST("/embeddings", bodyLimit, textBodyLimit, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, embeddingsHandler)
		gateway.POST("/images/generations", bodyLimit, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, imagesHandler)
		gateway.POST("/images/edits", bodyLimit, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, imagesHandler)
		gateway.POST("/images/generations/async", bodyLimit, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, h.AsyncImage.Submit)
		gateway.POST("/images/edits/async", bodyLimit, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, h.AsyncImage.Submit)
		gateway.POST("/images/batches", bodyLimit, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, h.BatchImage.Submit)
		gateway.POST("/videos/generations", bodyLimit, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, videoGenerationHandler)
		gateway.POST("/videos/edits", bodyLimit, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, videoEditHandler)
		gateway.POST("/videos/extensions", bodyLimit, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, videoExtensionHandler)
	}
	r.POST("/v1/messages/count_tokens", clientRequestID, opsErrorLogger, modelTraceDeferred, bodyLimit, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, countTokensHandler)
	{
		// Control-plane routes retain their previous middleware order and never install Candidate.
		gateway := r.Group("/v1", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic)
		gateway.GET("/models", modelsHandler)
		gateway.GET("/usage", h.Gateway.Usage)
		gateway.GET("/live/:call_id", h.OpenAIGateway.LiveSideband)
		gateway.GET("/responses", responsesWebSocketHandler)
		gateway.GET("/images/tasks/:task_id", h.AsyncImage.Get)
		gateway.GET("/images/batches", h.BatchImage.List)
		gateway.GET("/images/batches/models", h.BatchImage.Models)
		gateway.GET("/images/batches/:id", h.BatchImage.Get)
		gateway.GET("/images/batches/:id/items", h.BatchImage.Items)
		gateway.GET("/images/batches/:id/items/:custom_id/content", h.BatchImage.ItemContent)
		gateway.GET("/images/batches/:id/download", h.BatchImage.Download)
		gateway.POST("/images/batches/:id/cancel", h.BatchImage.Cancel)
		gateway.DELETE("/images/batches/:id", h.BatchImage.DeleteRecord)
		gateway.DELETE("/images/batches/:id/outputs", h.BatchImage.DeleteOutputs)
		gateway.GET("/videos/:request_id", videoStatusHandler)
		gateway.GET("/videos/:request_id/content", videoContentHandler)
	}

	// Gemini native API compatibility layer.
	{
		gemini := r.Group("/v1beta", clientRequestID, opsErrorLogger, modelTraceCandidate, bodyLimit, endpointNorm, googleAPIKeyAuth, compositeGeminiTarget, requireGroupGoogle)
		// Gin treats ":" as a param marker, but Gemini uses "{model}:{action}" in the same segment.
		gemini.POST("/models/*modelAction", h.Gateway.GeminiV1BetaModels)
	}
	{
		gemini := r.Group("/v1beta", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, googleAPIKeyAuth, compositeGeminiTarget, requireGroupGoogle)
		gemini.GET("/models", h.Gateway.GeminiV1BetaListModels)
		gemini.GET("/models/:model", h.Gateway.GeminiV1BetaGetModel)
	}

	// Root aliases: candidates install Candidate before the applicable body limit and auth chain.
	r.POST("/responses", clientRequestID, opsErrorLogger, modelTraceCandidate, bodyLimit, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, responsesHandler)
	r.POST("/responses/*subpath", clientRequestID, opsErrorLogger, modelTraceCandidate, bodyLimit, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, guardResponsesSubpath(responsesHandler))
	r.POST("/alpha/search", clientRequestID, opsErrorLogger, modelTraceCandidate, textBodyLimit, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, h.OpenAIGateway.AlphaSearch)
	r.POST("/chat/completions", clientRequestID, opsErrorLogger, modelTraceCandidate, bodyLimit, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, chatCompletionsHandler)
	r.POST("/embeddings", clientRequestID, opsErrorLogger, modelTraceCandidate, textBodyLimit, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, embeddingsHandler)
	r.POST("/images/generations", clientRequestID, opsErrorLogger, modelTraceCandidate, bodyLimit, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, imagesHandler)
	r.POST("/images/edits", clientRequestID, opsErrorLogger, modelTraceCandidate, bodyLimit, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, imagesHandler)
	r.POST("/images/generations/async", clientRequestID, opsErrorLogger, modelTraceCandidate, bodyLimit, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, h.AsyncImage.Submit)
	r.POST("/images/edits/async", clientRequestID, opsErrorLogger, modelTraceCandidate, bodyLimit, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, h.AsyncImage.Submit)
	r.POST("/videos/generations", clientRequestID, opsErrorLogger, modelTraceCandidate, bodyLimit, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, videoGenerationHandler)
	r.POST("/videos/edits", clientRequestID, opsErrorLogger, modelTraceCandidate, bodyLimit, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, videoEditHandler)
	r.POST("/videos/extensions", clientRequestID, opsErrorLogger, modelTraceCandidate, bodyLimit, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, videoExtensionHandler)
	r.GET("/responses", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, responsesWebSocketHandler)
	r.GET("/models", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, modelsHandler)
	r.POST("/messages/count_tokens", clientRequestID, opsErrorLogger, modelTraceDeferred, bodyLimit, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, countTokensHandler)
	r.GET("/images/tasks/:task_id", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, h.AsyncImage.Get)
	r.GET("/videos/:request_id", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, videoStatusHandler)
	r.GET("/videos/:request_id/content", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, videoContentHandler)

	// Codex direct aliases.
	{
		codexDirect := r.Group("/backend-api/codex", clientRequestID, opsErrorLogger, modelTraceCandidate)
		codexDirect.POST("/realtime/calls", bodyLimit, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, h.OpenAIGateway.Live)
		codexDirect.POST("/responses", bodyLimit, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, responsesHandler)
		codexDirect.POST("/responses/*subpath", bodyLimit, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, guardResponsesSubpath(responsesHandler))
		codexDirect.POST("/alpha/search", bodyLimit, textBodyLimit, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic, h.OpenAIGateway.AlphaSearch)
	}
	{
		codexDirect := r.Group("/backend-api/codex", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, apiKeyAuthHandler, compositeTarget, requireGroupAnthropic)
		codexDirect.GET("/:call_id", h.OpenAIGateway.LiveSideband)
		codexDirect.GET("/responses", responsesWebSocketHandler)
		codexDirect.GET("/models", h.OpenAIGateway.CodexModels)
	}

	// Antigravity model list remains a control-plane route with its historical chain.
	r.GET("/antigravity/models", apiKeyAuthHandler, requireGroupAnthropic, h.Gateway.AntigravityModels)

	// Antigravity Anthropic-compatible routes.
	{
		antigravityV1 := r.Group("/antigravity/v1", clientRequestID, opsErrorLogger, modelTraceCandidate, bodyLimit, endpointNorm, middleware.ForcePlatform(service.PlatformAntigravity), apiKeyAuthHandler, requireGroupAnthropic)
		antigravityV1.POST("/messages", h.Gateway.Messages)
	}
	r.POST("/antigravity/v1/messages/count_tokens", clientRequestID, opsErrorLogger, modelTraceDeferred, bodyLimit, endpointNorm, middleware.ForcePlatform(service.PlatformAntigravity), apiKeyAuthHandler, requireGroupAnthropic, countTokensHandler)
	{
		antigravityV1 := r.Group("/antigravity/v1", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, middleware.ForcePlatform(service.PlatformAntigravity), apiKeyAuthHandler, requireGroupAnthropic)
		antigravityV1.GET("/models", h.Gateway.AntigravityModels)
		antigravityV1.GET("/usage", h.Gateway.Usage)
	}

	// Antigravity Gemini-compatible routes.
	{
		antigravityV1Beta := r.Group("/antigravity/v1beta", clientRequestID, opsErrorLogger, modelTraceCandidate, bodyLimit, endpointNorm, middleware.ForcePlatform(service.PlatformAntigravity), googleAPIKeyAuth, requireGroupGoogle)
		antigravityV1Beta.POST("/models/*modelAction", h.Gateway.GeminiV1BetaModels)
	}
	{
		antigravityV1Beta := r.Group("/antigravity/v1beta", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, middleware.ForcePlatform(service.PlatformAntigravity), googleAPIKeyAuth, requireGroupGoogle)
		antigravityV1Beta.GET("/models", h.Gateway.GeminiV1BetaListModels)
		antigravityV1Beta.GET("/models/:model", h.Gateway.GeminiV1BetaGetModel)
	}

}

// getGroupPlatform extracts the group platform from the API Key stored in context.
func getGroupPlatform(c *gin.Context) string {
	apiKey, ok := middleware.GetAPIKeyFromContext(c)
	if !ok || apiKey.Group == nil {
		return ""
	}
	if apiKey.Group.Platform == service.PlatformComposite {
		if platform, ok := service.ResolvedTargetPlatformFromContext(c.Request.Context()); ok {
			return platform
		}
	}
	return apiKey.Group.Platform
}

func compositeTargetPlatformMiddleware(resolver *service.CompositeRouteResolver) gin.HandlerFunc {
	if resolver == nil {
		resolver = service.NewCompositeRouteResolver(nil)
	}
	return func(c *gin.Context) {
		apiKey, ok := middleware.GetAPIKeyFromContext(c)
		if !ok || apiKey == nil || apiKey.Group == nil || apiKey.Group.Platform != service.PlatformComposite {
			c.Next()
			return
		}
		if c.Request == nil || c.Request.Method == http.MethodGet {
			c.Next()
			return
		}

		body, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
		if err != nil {
			status := http.StatusBadRequest
			message := "Failed to read request body"
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				status = http.StatusRequestEntityTooLarge
				message = "Request body is too large"
			}
			c.JSON(status, gin.H{"error": gin.H{"type": "invalid_request_error", "message": message}})
			c.Abort()
			return
		}

		model := compositeRequestModelFromBody(c.GetHeader("Content-Type"), body)
		if model != "" {
			decision, err := resolver.Resolve(c.Request.Context(), apiKey.Group.ID, model, compositeRouteEndpointForPath(c.Request.URL.Path))
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "server_error", "message": "Failed to resolve composite model route"}})
				c.Abort()
				return
			}
			if decision.Matched {
				c.Request = c.Request.WithContext(service.WithCompositeRouteDecision(c.Request.Context(), decision))
				if upstreamModel := strings.TrimSpace(decision.UpstreamModel); upstreamModel != "" && upstreamModel != model && gjson.ValidBytes(body) {
					if rewritten, rewriteErr := sjson.SetBytes(body, "model", upstreamModel); rewriteErr == nil {
						body = rewritten
					}
				}
			}
		}
		resetRequestBody(c, body)
		c.Next()
	}
}

func compositeRequestModelFromBody(contentType string, body []byte) string {
	if model := strings.TrimSpace(gjson.GetBytes(body, "model").String()); model != "" {
		return model
	}
	return compositeMultipartModelFromBody(contentType, body)
}

func compositeMultipartModelFromBody(contentType string, body []byte) string {
	mediaType, params, err := mime.ParseMediaType(strings.TrimSpace(contentType))
	if err != nil || !strings.EqualFold(mediaType, "multipart/form-data") {
		return ""
	}
	boundary := strings.TrimSpace(params["boundary"])
	if boundary == "" {
		return ""
	}
	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			return ""
		}
		if err != nil {
			return ""
		}
		if part.FormName() != "model" || part.FileName() != "" {
			continue
		}
		data, err := io.ReadAll(part)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(data))
	}
}

func compositeGeminiTargetPlatformMiddleware(resolver *service.CompositeRouteResolver) gin.HandlerFunc {
	if resolver == nil {
		resolver = service.NewCompositeRouteResolver(nil)
	}
	return func(c *gin.Context) {
		apiKey, ok := middleware.GetAPIKeyFromContext(c)
		if ok && apiKey != nil && apiKey.Group != nil && apiKey.Group.Platform == service.PlatformComposite {
			model := compositeGeminiModelFromParams(c)
			if model != "" {
				decision, err := resolver.Resolve(c.Request.Context(), apiKey.Group.ID, model, service.CompositeRouteEndpointGemini)
				if err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "server_error", "message": "Failed to resolve composite model route"}})
					c.Abort()
					return
				}
				if decision.Matched {
					c.Request = c.Request.WithContext(service.WithCompositeRouteDecision(c.Request.Context(), decision))
				}
			}
			if _, resolved := service.ResolvedTargetPlatformFromContext(c.Request.Context()); !resolved {
				c.Request = c.Request.WithContext(service.WithResolvedTargetPlatform(c.Request.Context(), service.PlatformGemini))
			}
		}
		c.Next()
	}
}

// grokCustomVoiceEndpoint derives the upstream Voice endpoint for the
// /custom-voices/:voice_id[/audio] routes.
//
// The /audio suffix must be decided from the matched route template, not from
// the raw URL path: a voice literally named "audio" makes GET
// /custom-voices/audio match /custom-voices/:voice_id, and a raw-path suffix
// check would rewrite it to custom-voices/audio/audio — turning a profile
// lookup into an audio download.
func grokCustomVoiceEndpoint(c *gin.Context) string {
	endpoint := "custom-voices/" + c.Param("voice_id")
	if strings.HasSuffix(c.FullPath(), "/:voice_id/audio") {
		endpoint += "/audio"
	}
	return endpoint
}

func compositeGeminiModelFromParams(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if model := strings.TrimSpace(c.Param("model")); model != "" {
		return model
	}
	modelAction := strings.TrimPrefix(strings.TrimSpace(c.Param("modelAction")), "/")
	if modelAction == "" {
		return ""
	}
	if idx := strings.LastIndex(modelAction, ":"); idx >= 0 {
		return strings.TrimSpace(modelAction[:idx])
	}
	return modelAction
}

func resetRequestBody(c *gin.Context, body []byte) {
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	c.Request.ContentLength = int64(len(body))
	c.Request.Header.Set("Content-Length", strconv.Itoa(len(body)))
}

func compositeRouteEndpointForPath(path string) string {
	switch {
	case strings.Contains(path, "/messages/count_tokens"):
		return service.CompositeRouteEndpointCountTokens
	case strings.Contains(path, "/messages"):
		return service.CompositeRouteEndpointMessages
	case strings.Contains(path, "/responses"):
		return service.CompositeRouteEndpointResponses
	case strings.Contains(path, "/chat/completions"):
		return service.CompositeRouteEndpointChatCompletions
	case strings.Contains(path, "/embeddings"):
		return service.CompositeRouteEndpointEmbeddings
	case strings.Contains(path, "/images/"):
		return service.CompositeRouteEndpointImages
	case strings.Contains(path, "/v1beta/"):
		return service.CompositeRouteEndpointGemini
	default:
		return service.CompositeRouteEndpointAny
	}
}
