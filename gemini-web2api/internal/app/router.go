package app

import (
	"net/http"
	"strings"

	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/accounts"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/adapter"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/balancer"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/mcp"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/requestdb"
	"github.com/gin-gonic/gin"
)

// Deps 是路由使用的运行期组件
type Deps struct {
	Pool     *balancer.AccountPool
	Store    *requestdb.Store
	Accounts *accounts.Manager
}

// NewRouter 注册就绪检查与全部兼容 API（进程内插件 RPC 专用）
func NewRouter(deps Deps) *gin.Engine {
	pool := deps.Pool
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(gin.Recovery())
	router.GET("/readyz", func(c *gin.Context) {
		status := pool.Status()
		body := gin.H{"status": "ready", "accounts": status.Total, "available": status.Available, "version": Version()}
		if !status.Ready() {
			body["status"] = "unavailable"
			c.JSON(http.StatusServiceUnavailable, body)
			return
		}
		c.JSON(http.StatusOK, body)
	})

	api := router.Group("")
	api.Use(adapter.RequestMiddleware(deps.Store))
	api.POST("/v1/chat/completions", adapter.ChatCompletionHandler(pool))
	api.POST("/v1/responses", adapter.ResponsesHandler(pool, deps.Store))
	api.POST("/v1/images/generations", adapter.ImageGenerationHandler(pool))
	api.POST("/v1/images/edits", adapter.ImageEditHandler(pool))
	api.GET("/v1/models", adapter.GeminiOr(adapter.GeminiListModelsHandler(pool), adapter.ListModelsHandler(pool)))
	api.GET("/v1/models/:model", adapter.GeminiOr(adapter.GeminiGetModelHandler(pool), adapter.RetrieveModelHandler(pool)))
	api.POST("/v1/models/*action", adapter.GeminiRouterHandler(pool, deps.Store))
	api.POST("/v1/messages", adapter.ClaudeMessagesHandler(pool))
	api.POST("/v1/messages/count_tokens", adapter.ClaudeCountTokensHandler(pool))
	api.POST("/v1beta/models/*action", adapter.GeminiRouterHandler(pool, deps.Store))
	api.GET("/v1beta/models", adapter.GeminiListModelsHandler(pool))
	api.GET("/v1beta/models/:model", adapter.GeminiGetModelHandler(pool))
	for _, version := range []string{"/v1", "/v1beta"} {
		api.POST(version+"/interactions", adapter.InteractionsHandler(pool, deps.Store))
		api.GET(version+"/interactions/:interaction", adapter.InteractionRetrieveHandler(deps.Store))
		api.DELETE(version+"/interactions/:interaction", adapter.InteractionDeleteHandler(deps.Store))
		api.POST(version+"/interactions/:interaction/cancel", adapter.InteractionCancelHandler(deps.Store))
		api.POST(version+"/cachedContents", adapter.GeminiCacheCreateHandler(pool))
		api.GET(version+"/cachedContents", adapter.GeminiCacheListHandler())
		api.GET(version+"/cachedContents/:cache", adapter.GeminiCacheGetHandler())
		api.PATCH(version+"/cachedContents/:cache", adapter.GeminiCacheUpdateHandler())
		api.DELETE(version+"/cachedContents/:cache", adapter.GeminiCacheDeleteHandler())
	}
	api.GET("/v1/responses/:response", adapter.ResponseRetrieveHandler(deps.Store))
	api.DELETE("/v1/responses/:response", adapter.ResponseDeleteHandler(deps.Store))
	api.GET("/v1/responses/:response/input_items", adapter.ResponseInputItemsHandler(deps.Store))
	api.POST("/v1/responses/:response/cancel", adapter.ResponseCancelHandler(deps.Store))
	api.POST("/v1/responses/input_tokens", adapter.ResponsesInputTokensHandler(pool, deps.Store))
	api.POST("/v1/responses/compact", adapter.ResponsesCompactHandler(pool, deps.Store))
	api.POST("/v1/completions", adapter.CompletionHandler(pool))
	api.POST("/v1/files", adapter.FileUploadHandler(deps.Store))
	api.GET("/v1/files", adapter.GeminiOr(adapter.GeminiFileListHandler(deps.Store), adapter.FileListHandler(deps.Store)))
	api.GET("/v1/files/:file", adapter.GeminiOr(adapter.GeminiFileGetHandler(deps.Store), adapter.FileRetrieveHandler(deps.Store)))
	api.DELETE("/v1/files/:file", adapter.GeminiOr(adapter.GeminiFileDeleteHandler(deps.Store), adapter.FileDeleteHandler(deps.Store)))
	api.GET("/v1/files/:file/content", adapter.FileContentHandler(deps.Store))
	api.POST("/upload/v1/files", adapter.GeminiFileUploadHandler(deps.Store))
	api.POST("/upload/v1beta/files", adapter.GeminiFileUploadHandler(deps.Store))
	api.PUT("/upload/v1beta/files", adapter.GeminiFileUploadHandler(deps.Store))
	api.GET("/v1beta/files", adapter.GeminiFileListHandler(deps.Store))
	api.GET("/v1beta/files/:file", adapter.GeminiFileGetHandler(deps.Store))
	api.DELETE("/v1beta/files/:file", adapter.GeminiFileDeleteHandler(deps.Store))
	api.POST("/v1/videos", adapter.VideoCreateHandler(pool, deps.Store))
	api.GET("/v1/videos", adapter.VideoListHandler(deps.Store))
	api.GET("/v1/videos/:video", adapter.VideoRetrieveHandler(deps.Store))
	api.DELETE("/v1/videos/:video", adapter.VideoDeleteHandler(deps.Store))
	api.GET("/v1/videos/:video/content", adapter.VideoContentHandler(deps.Store))
	api.POST("/v1/videos/:video/remix", adapter.VideoRemixHandler(pool, deps.Store))
	api.POST("/v1/videos/extensions", adapter.VideoExtensionHandler(pool, deps.Store))
	api.GET("/v1beta/models/:model/operations/:operation", adapter.GeminiOperationHandler(deps.Store))
	api.GET("/v1beta/operations/:operation", adapter.GeminiOperationHandler(deps.Store))
	api.GET("/v1/models/:model/operations/:operation", adapter.GeminiOperationHandler(deps.Store))
	api.GET("/v1/operations/:operation", adapter.GeminiOperationHandler(deps.Store))
	api.POST("/v1/audio/speech", adapter.SpeechHandler(pool))
	api.POST("/v1/audio/transcriptions", adapter.TranscriptionHandler(pool))
	api.POST("/v1/audio/translations", adapter.TranslationHandler(pool))
	api.POST("/v1/embeddings", adapter.UnavailableOpenAIHandler("embeddings", "embeddings"))
	api.POST("/v1/moderations", adapter.UnavailableOpenAIHandler("moderations", "moderation"))
	api.GET("/v1/realtime", adapter.UnavailableOpenAIHandler("realtime", "the Realtime API"))
	api.POST("/v1/realtime/sessions", adapter.UnavailableOpenAIHandler("realtime", "the Realtime API"))
	api.POST("/v1/realtime/client_secrets", adapter.UnavailableOpenAIHandler("realtime", "the Realtime API"))
	api.GET("/ws/*method", adapter.GeminiLiveHandler)
	mcpHandler := mcp.Handler(router, pool, Version())
	api.POST("/mcp", mcpHandler)
	api.GET("/mcp", mcpHandler)
	api.DELETE("/mcp", mcpHandler)

	router.NoRoute(adapter.RequestMiddleware(deps.Store), func(c *gin.Context) {
		path := c.Request.URL.Path
		switch {
		case adapter.IsGeminiRequest(c):
			adapter.WriteGeminiNotFound(c)
		case strings.HasPrefix(path, "/v1/"):
			adapter.WriteOpenAINotFound(c)
		default:
			adapter.WriteOpenAINotFound(c)
		}
	})
	return router
}
