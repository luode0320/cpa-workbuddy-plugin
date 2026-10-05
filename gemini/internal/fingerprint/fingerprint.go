package fingerprint

import (
	"fmt"
	"runtime"
	"strings"
)

const (
	GeminiCLIVersion = "0.34.0"
	APIClientHeader  = "google-genai-sdk/1.41.0 gl-node/v22.19.0"
)

func UserAgent(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		model = "unknown"
	}
	return fmt.Sprintf("GeminiCLI/%s/%s (%s; %s; terminal)", GeminiCLIVersion, model, geminiCLIOS(), geminiCLIArch())
}

func geminiCLIOS() string {
	if runtime.GOOS == "windows" {
		return "win32"
	}
	return runtime.GOOS
}

func geminiCLIArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x64"
	case "386":
		return "x86"
	default:
		return runtime.GOARCH
	}
}
