package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorapi"
	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorauth"
	"github.com/luode0320/cpa-workbuddy-plugin/cursor/internal/cursorproto"
)

type CursorClient interface {
	Run(context.Context, cursorapi.RunInput, func(cursorproto.ServerEvent) error) (cursorapi.RunResult, error)
	DiscoverModels(context.Context, string) ([]string, error)
}

type StreamEmitter interface {
	Emit(context.Context, string, []byte) error
	Close(string, error) error
}

type HostCaller interface {
	Call(context.Context, string, any) (json.RawMessage, error)
}

type Dependencies struct {
	Auth    *cursorauth.Service
	Cursor  CursorClient
	Emitter StreamEmitter
	Host    HostCaller
}

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code                 string `json:"code"`
	Message              string `json:"message"`
	Retryable            bool   `json:"retryable,omitempty"`
	HTTPStatus           int    `json:"http_status,omitempty"`
	RequestScoped        bool   `json:"request_scoped,omitempty"`
	OutputExposed        bool   `json:"output_exposed,omitempty"`
	ToolExposed          bool   `json:"tool_exposed,omitempty"`
	InteractionResponded bool   `json:"interaction_responded,omitempty"`
}

type authData struct {
	Provider         string            `json:"Provider"`
	ID               string            `json:"ID"`
	FileName         string            `json:"FileName"`
	Label            string            `json:"Label"`
	StorageJSON      []byte            `json:"StorageJSON"`
	Metadata         map[string]string `json:"Metadata,omitempty"`
	Attributes       map[string]string `json:"Attributes,omitempty"`
	NextRefreshAfter time.Time         `json:"NextRefreshAfter,omitempty"`
}

type modelInfo struct {
	ID                         string   `json:"ID"`
	Object                     string   `json:"Object"`
	OwnedBy                    string   `json:"OwnedBy"`
	DisplayName                string   `json:"DisplayName"`
	SupportedGenerationMethods []string `json:"SupportedGenerationMethods"`
	SupportedInputModalities   []string `json:"SupportedInputModalities"`
	SupportedOutputModalities  []string `json:"SupportedOutputModalities"`
	ContextLength              int64    `json:"ContextLength,omitempty"`
	NativeContextLength        int64    `json:"NativeContextLength,omitempty"`
	ClientContextLimit         int64    `json:"ClientContextLimit"`
	MaxCompletionTokens        int64    `json:"MaxCompletionTokens,omitempty"`
	UserDefined                bool     `json:"UserDefined"`
}

type executorRequest struct {
	AuthID          string            `json:"AuthID"`
	AuthProvider    string            `json:"AuthProvider"`
	Model           string            `json:"Model"`
	Format          string            `json:"Format"`
	Stream          bool              `json:"Stream"`
	Alt             string            `json:"Alt"`
	Headers         http.Header       `json:"Headers"`
	Query           url.Values        `json:"Query"`
	OriginalRequest []byte            `json:"OriginalRequest"`
	SourceFormat    string            `json:"SourceFormat"`
	ResponseFormat  string            `json:"ResponseFormat"`
	Payload         []byte            `json:"Payload"`
	Metadata        executorMetadata  `json:"Metadata"`
	StorageJSON     []byte            `json:"StorageJSON"`
	AuthMetadata    json.RawMessage   `json:"AuthMetadata"`
	AuthAttributes  map[string]string `json:"AuthAttributes"`
	StreamID        string            `json:"stream_id"`
}

type executorResponse struct {
	Payload []byte      `json:"Payload"`
	Headers http.Header `json:"Headers,omitempty"`
}
