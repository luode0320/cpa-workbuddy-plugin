package adapter

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/balancer"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/gemini"
)

const (
	// remoteImageUploadLimit 是消息内联远程图片下载的内存上限（50 MiB）
	remoteImageUploadLimit = 50 << 20
	// remoteMediaUploadLimit 是消息内联远程音视频/文档下载的内存上限（100 MiB）
	remoteMediaUploadLimit = 100 << 20
)

// errNoMediaClient 表示没有就绪账号可以下载远程媒体
var errNoMediaClient = errors.New("没有就绪账号可以下载远程媒体")

// fetchRemoteImage 下载客户端给出的 http(s) 图片地址，只连接公网地址，限制单图不超过 remoteImageUploadLimit
func fetchRemoteImage(ctx context.Context, client *gemini.Client, rawURL string) ([]byte, error) {
	return fetchRemoteMediaWithLimit(ctx, client, rawURL, remoteImageUploadLimit)
}

// fetchRemoteMedia 经 client 的浏览器身份与出口下载客户端给出的 http(s) 媒体地址，只连接公网地址，大小不超过 remoteMediaUploadLimit
func fetchRemoteMedia(ctx context.Context, client *gemini.Client, rawURL string) ([]byte, error) {
	return fetchRemoteMediaWithLimit(ctx, client, rawURL, remoteMediaUploadLimit)
}

func fetchRemoteMediaWithLimit(ctx context.Context, client *gemini.Client, rawURL string, limit int64) ([]byte, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("媒体地址必须是 http 或 https: %s", rawURL)
	}
	if client == nil {
		return nil, errNoMediaClient
	}
	return client.FetchRemoteMedia(ctx, rawURL, limit)
}
// countClient 返回第一个就绪账号的客户端，计数入口用它下载远程媒体
func countClient(pool *balancer.AccountPool) *gemini.Client {
	for _, account := range pool.CatalogAccounts() {
		if account.Ready {
			return account.Client
		}
	}
	return nil
}
