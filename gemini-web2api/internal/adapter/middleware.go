package adapter

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/gemini"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/logging"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/requestdb"
	"github.com/gin-gonic/gin"
)

// RequestMiddleware 为公开 API 请求分配 ID、跟踪生成请求的阶段，结束时输出结果并把生成请求写入账本，首次创建时从账本补入首事件样本
func RequestMiddleware(store *requestdb.Store) gin.HandlerFunc {
	if store != nil {
		go firstEvents.seed(store)
	}
	return func(c *gin.Context) {
		started := time.Now()
		request, ctx := logging.NewRequest(c.Request.Context(), c.Request.Method, c.Request.URL.Path, c.ClientIP())
		c.Request = c.Request.WithContext(ctx)
		c.Header("X-Request-ID", request.ID())
		var capture *bodyCapture
		if store.BodyCapture() {
			capture = captureBodies(c)
		}
		c.Next()
		status := c.Writer.Status()
		info := request.Finish(status, ctx.Err() != nil)
		if request.Begun() {
			store.Record(info, capture.body())
			return
		}
		target := c.Request.Method + " " + c.Request.URL.Path
		switch {
		case status == http.StatusNotFound && c.FullPath() == "":
			logging.Warn(logging.SourceRequest, "未知路由", "", target, "IP", c.ClientIP())
		case status >= 400:
			logging.Warn(logging.SourceRequest, "请求被拒绝", "", target, "", status, "", info.Error)
		default:
			logging.Debug(logging.SourceRequest, "访问", "", target, "", status, "", time.Since(started))
		}
	}
}

// bodyCapture 保存请求与响应正文的前 requestdb.BodyLimit 字节与原始字节数
type bodyCapture struct {
	gin.ResponseWriter
	request  limitedBuffer
	response limitedBuffer
}

// limitedBuffer 只保留前 requestdb.BodyLimit 字节并统计总字节数
type limitedBuffer struct {
	data  bytes.Buffer
	total int64
}

func (buffer *limitedBuffer) Write(data []byte) (int, error) {
	buffer.total += int64(len(data))
	if room := requestdb.BodyLimit - buffer.data.Len(); room > 0 {
		buffer.data.Write(data[:min(len(data), room)])
	}
	return len(data), nil
}

// captureBodies 在请求体被读取与响应写出时同步记录正文
func captureBodies(c *gin.Context) *bodyCapture {
	capture := &bodyCapture{ResponseWriter: c.Writer}
	if c.Request.Body != nil {
		c.Request.Body = struct {
			io.Reader
			io.Closer
		}{io.TeeReader(c.Request.Body, &capture.request), c.Request.Body}
	}
	c.Writer = capture
	return capture
}

func (capture *bodyCapture) Write(data []byte) (int, error) {
	_, _ = capture.response.Write(data)
	return capture.ResponseWriter.Write(data)
}

func (capture *bodyCapture) WriteString(data string) (int, error) {
	_, _ = capture.response.Write([]byte(data))
	return capture.ResponseWriter.WriteString(data)
}

// body 返回捕获的正文，未捕获时为 nil
func (capture *bodyCapture) body() *requestdb.Body {
	if capture == nil {
		return nil
	}
	return &requestdb.Body{
		Request: capture.request.data.Bytes(), RequestSize: capture.request.total,
		Response: capture.response.data.Bytes(), ResponseSize: capture.response.total,
	}
}

// thinkingLevelNames 返回模型可选思考强度的小写名称
func thinkingLevelNames(levels []gemini.ThinkingLevel) []string {
	names := make([]string, 0, len(levels))
	for _, level := range levels {
		names = append(names, level.String())
	}
	return names
}

// IsGeminiRequest 判断请求是否属于 Gemini 协议，/v1 上的请求以模型方法路径或 x-goog-api-key 头识别
func IsGeminiRequest(c *gin.Context) bool {
	path := c.Request.URL.Path
	return strings.HasPrefix(path, "/v1beta/") || strings.HasPrefix(path, "/upload/") || strings.HasPrefix(path, "/ws/google.ai.generativelanguage.") ||
		strings.HasPrefix(path, "/v1/models/") && strings.Contains(path, ":") || strings.HasPrefix(path, "/v1/interactions") || strings.HasPrefix(path, "/v1/cachedContents") ||
		c.GetHeader("x-goog-api-key") != ""
}

// GeminiOr 在 Gemini 与 OpenAI 共用的路径上按请求协议选择处理函数
func GeminiOr(gemini, openAI gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		if IsGeminiRequest(c) {
			gemini(c)
			return
		}
		openAI(c)
	}
}
