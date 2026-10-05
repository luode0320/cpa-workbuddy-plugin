package compat

import (
	"bytes"
	"testing"

	"github.com/tidwall/gjson"
)

func TestWrapRequestUsesGeminiCLIEnvelope(t *testing.T) {
	body := []byte(`{"model":"ignored-model","contents":[{"role":"user","parts":[{"text":"hello"}]}],"system_instruction":{"parts":[{"text":"system"}]}}`)

	wrapped := WrapRequest("gemini-2.5-pro", body)
	if got := gjson.GetBytes(wrapped, "model").String(); got != "gemini-2.5-pro" {
		t.Fatalf("model = %q, want gemini-2.5-pro", got)
	}
	if !gjson.GetBytes(wrapped, "request.contents.0.parts.0.text").Exists() {
		t.Fatalf("wrapped request does not contain contents: %s", wrapped)
	}
	if gjson.GetBytes(wrapped, "request.model").Exists() {
		t.Fatalf("wrapped request still contains request.model: %s", wrapped)
	}
	if !gjson.GetBytes(wrapped, "request.systemInstruction.parts.0.text").Exists() {
		t.Fatalf("wrapped request does not contain systemInstruction: %s", wrapped)
	}
	if gjson.GetBytes(wrapped, "request.system_instruction").Exists() {
		t.Fatalf("wrapped request still contains system_instruction: %s", wrapped)
	}
}

func TestWrapRequestNormalizesToolsAndThoughtSignatures(t *testing.T) {
	body := []byte(`{
		"model":"ignored-model",
		"contents":[
			{"role":"model","parts":[{"functionCall":{"id":"1 call id","name":"1 bad name!","args":{}},"thought_signature":"nested-signature"}]},
			{"role":"user","parts":[{"functionResponse":{"id":"1 call id","response":{"result":"ok"},"thoughtSignature":"drop-me"}}]},
			{"role":"user","parts":[]}
		],
		"tools":[{
			"functionDeclarations":[{
				"name":"1 bad name!",
				"strict":true,
				"parameters":{
					"$schema":"https://json-schema.org/draft/2020-12/schema",
					"type":"object",
					"title":"Bad",
					"properties":{
						"count":{"type":["integer","null"],"nullable":true,"uniqueItems":true},
						"missingRequired":{"type":"string"}
					},
					"required":["count","ghost"],
					"x-extra":"drop"
				}
			}]
		}]
	}`)

	wrapped := WrapRequest("gemini-2.5-pro", body)
	if got := gjson.GetBytes(wrapped, "request.tools.0.function_declarations.0.name").String(); got != "_1_bad_name_" {
		t.Fatalf("tool name = %q, want _1_bad_name_: %s", got, wrapped)
	}
	if gjson.GetBytes(wrapped, "request.tools.0.functionDeclarations").Exists() {
		t.Fatalf("camel functionDeclarations survived: %s", wrapped)
	}
	if gjson.GetBytes(wrapped, "request.tools.0.function_declarations.0.parameters").Exists() {
		t.Fatalf("parameters survived: %s", wrapped)
	}
	schema := gjson.GetBytes(wrapped, "request.tools.0.function_declarations.0.parametersJsonSchema")
	for _, path := range []string{"$schema", "title", "properties.count.nullable", "properties.count.uniqueItems", "x-extra"} {
		if schema.Get(path).Exists() {
			t.Fatalf("unsupported schema path %q survived in %s", path, schema.Raw)
		}
	}
	if got := schema.Get("required.0").String(); got != "count" {
		t.Fatalf("required[0] = %q, want count in %s", got, schema.Raw)
	}
	if got := gjson.GetBytes(wrapped, "request.contents.0.parts.0.functionCall.name").String(); got != "_1_bad_name_" {
		t.Fatalf("functionCall name = %q, want _1_bad_name_", got)
	}
	if got := gjson.GetBytes(wrapped, "request.contents.0.parts.0.functionCall.id").String(); got != "_1_call_id" {
		t.Fatalf("functionCall id = %q, want _1_call_id", got)
	}
	if got := gjson.GetBytes(wrapped, "request.contents.0.parts.0.thoughtSignature").String(); got != "nested-signature" {
		t.Fatalf("thoughtSignature = %q, want nested-signature", got)
	}
	if got := gjson.GetBytes(wrapped, "request.contents.1.role").String(); got != "user" {
		t.Fatalf("response role = %q, want user", got)
	}
	if got := gjson.GetBytes(wrapped, "request.contents.1.parts.0.functionResponse.name").String(); got != "_1_bad_name_" {
		t.Fatalf("functionResponse name = %q, want _1_bad_name_", got)
	}
	if gjson.GetBytes(wrapped, "request.contents.1.parts.0.thoughtSignature").Exists() {
		t.Fatalf("functionResponse thoughtSignature survived: %s", wrapped)
	}
	if got := gjson.GetBytes(wrapped, "request.contents.#").Int(); got != 2 {
		t.Fatalf("contents count = %d, want 2: %s", got, wrapped)
	}
}

func TestUnwrapRequestRestoresGeminiShape(t *testing.T) {
	wrapped := []byte(`{"project":"project-a","model":"gemini-2.5-pro","request":{"contents":[{"parts":[{"text":"hello"}]}],"systemInstruction":{"parts":[{"text":"system"}]}}}`)

	unwrapped := UnwrapRequest(wrapped)
	if got := gjson.GetBytes(unwrapped, "model").String(); got != "gemini-2.5-pro" {
		t.Fatalf("model = %q, want gemini-2.5-pro", got)
	}
	if !gjson.GetBytes(unwrapped, "system_instruction.parts.0.text").Exists() {
		t.Fatalf("unwrapped request does not contain system_instruction: %s", unwrapped)
	}
	if gjson.GetBytes(unwrapped, "systemInstruction").Exists() {
		t.Fatalf("unwrapped request still contains systemInstruction: %s", unwrapped)
	}
}

func TestUnwrapResponseHandlesSSEDataAndDone(t *testing.T) {
	unwrapped := UnwrapResponse([]byte(`data: {"response":{"candidates":[{"content":{"parts":[{"text":"hello"}]}}]}}`))
	if got := gjson.GetBytes(unwrapped, "candidates.0.content.parts.0.text").String(); got != "hello" {
		t.Fatalf("response text = %q, want hello", got)
	}
	if done := UnwrapResponse([]byte(`data: [DONE]`)); !bytes.Equal(done, []byte("[DONE]")) {
		t.Fatalf("done payload = %s, want [DONE]", done)
	}
}
