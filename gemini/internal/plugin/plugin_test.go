package plugin

import "testing"

func TestBuildDeclaresGeminiCLICapabilities(t *testing.T) {
	plugin := Build(nil)
	if plugin.Metadata.Name != "Gemini CLI Provider" {
		t.Fatalf("metadata name = %q", plugin.Metadata.Name)
	}
	if plugin.Capabilities.AuthProvider == nil {
		t.Fatal("auth provider capability is nil")
	}
	if plugin.Capabilities.ModelProvider == nil {
		t.Fatal("model provider capability is nil")
	}
	if plugin.Capabilities.Executor == nil {
		t.Fatal("executor capability is nil")
	}
	if plugin.Capabilities.RequestTranslator == nil || plugin.Capabilities.ResponseTranslator == nil {
		t.Fatal("translator capability is nil")
	}
	if plugin.Capabilities.ThinkingApplier == nil {
		t.Fatal("thinking applier capability is nil")
	}
	if plugin.Capabilities.CommandLinePlugin == nil {
		t.Fatal("command line plugin capability is nil")
	}
	if len(plugin.Capabilities.ExecutorInputFormats) != 1 || plugin.Capabilities.ExecutorInputFormats[0] != executorFormat {
		t.Fatalf("executor input formats = %#v", plugin.Capabilities.ExecutorInputFormats)
	}
	if len(plugin.Capabilities.ExecutorOutputFormats) != 1 || plugin.Capabilities.ExecutorOutputFormats[0] != executorFormat {
		t.Fatalf("executor output formats = %#v", plugin.Capabilities.ExecutorOutputFormats)
	}
}
