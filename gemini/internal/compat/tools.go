package compat

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var functionNameSanitizer = regexp.MustCompile(`[^a-zA-Z0-9_.:-]`)

type functionCallGroup struct {
	responsesNeeded int
	callNames       []string
}

func NormalizeRequest(body []byte) []byte {
	body = normalizeRoles(body)
	body = normalizeToolDeclarations(body)
	body = normalizeFunctionParts(body)
	body = groupFunctionResponses(body)
	body = SanitizeThoughtSignatures(body, "request.contents")
	body = filterEmptyContents(body)
	body = attachDefaultSafetySettings(body)
	return body
}

func normalizeRoles(body []byte) []byte {
	contents := gjson.GetBytes(body, "request.contents")
	if !contents.IsArray() {
		return body
	}
	prevRole := ""
	idx := 0
	contents.ForEach(func(_, content gjson.Result) bool {
		role := content.Get("role").String()
		if role != "user" && role != "model" && role != "function" {
			if prevRole == "user" {
				role = "model"
			} else {
				role = "user"
			}
			body, _ = sjson.SetBytes(body, fmt.Sprintf("request.contents.%d.role", idx), role)
		}
		prevRole = role
		idx++
		return true
	})
	return body
}

func normalizeToolDeclarations(body []byte) []byte {
	tools := gjson.GetBytes(body, "request.tools")
	if !tools.IsArray() {
		return body
	}
	toolCount := len(tools.Array())
	for i := 0; i < toolCount; i++ {
		camelPath := fmt.Sprintf("request.tools.%d.functionDeclarations", i)
		snakePath := fmt.Sprintf("request.tools.%d.function_declarations", i)
		if camel := gjson.GetBytes(body, camelPath); camel.Exists() {
			body, _ = sjson.SetRawBytes(body, snakePath, []byte(camel.Raw))
			body, _ = sjson.DeleteBytes(body, camelPath)
		}
		declarations := gjson.GetBytes(body, snakePath)
		if !declarations.IsArray() {
			continue
		}
		declarationCount := len(declarations.Array())
		for j := 0; j < declarationCount; j++ {
			basePath := fmt.Sprintf("%s.%d", snakePath, j)
			name := gjson.GetBytes(body, basePath+".name").String()
			if name != "" {
				body, _ = sjson.SetBytes(body, basePath+".name", SanitizeFunctionName(name))
			}
			body, _ = sjson.DeleteBytes(body, basePath+".strict")
			body = normalizeToolSchema(body, basePath)
		}
	}
	return body
}

func normalizeToolSchema(body []byte, basePath string) []byte {
	parametersPath := basePath + ".parameters"
	schemaPath := basePath + ".parametersJsonSchema"
	if parameters := gjson.GetBytes(body, parametersPath); parameters.Exists() {
		body, _ = sjson.SetRawBytes(body, schemaPath, []byte(parameters.Raw))
		body, _ = sjson.DeleteBytes(body, parametersPath)
	}
	schema := gjson.GetBytes(body, schemaPath)
	if !schema.Exists() {
		body, _ = sjson.SetRawBytes(body, schemaPath, []byte(`{"type":"object","properties":{}}`))
		return body
	}
	cleaned := CleanJSONSchemaForGemini([]byte(schema.Raw))
	body, _ = sjson.SetRawBytes(body, schemaPath, cleaned)
	return body
}

func normalizeFunctionParts(body []byte) []byte {
	contents := gjson.GetBytes(body, "request.contents")
	if !contents.IsArray() {
		return body
	}
	contentCount := len(contents.Array())
	for i := 0; i < contentCount; i++ {
		parts := gjson.GetBytes(body, fmt.Sprintf("request.contents.%d.parts", i))
		if !parts.IsArray() {
			continue
		}
		partCount := len(parts.Array())
		for j := 0; j < partCount; j++ {
			partPath := fmt.Sprintf("request.contents.%d.parts.%d", i, j)
			if call := gjson.GetBytes(body, partPath+".functionCall"); call.Exists() {
				body = normalizeFunctionObject(body, partPath+".functionCall", call)
			}
			if response := gjson.GetBytes(body, partPath+".functionResponse"); response.Exists() {
				body = normalizeFunctionObject(body, partPath+".functionResponse", response)
			}
		}
	}
	return body
}

func normalizeFunctionObject(body []byte, path string, value gjson.Result) []byte {
	if name := value.Get("name").String(); name != "" {
		body, _ = sjson.SetBytes(body, path+".name", SanitizeFunctionName(name))
	}
	if id := firstNonEmpty(value.Get("id").String(), value.Get("call_id").String()); id != "" {
		body, _ = sjson.SetBytes(body, path+".id", SanitizeFunctionID(id))
		body, _ = sjson.DeleteBytes(body, path+".call_id")
	}
	return body
}

func groupFunctionResponses(body []byte) []byte {
	contents := gjson.GetBytes(body, "request.contents")
	if !contents.IsArray() {
		return body
	}
	out := []byte(`[]`)
	var pendingGroups []*functionCallGroup
	var collectedResponses []gjson.Result
	contents.ForEach(func(_, content gjson.Result) bool {
		parts := content.Get("parts")
		responses := functionResponseParts(parts)
		if len(responses) > 0 {
			collectedResponses = append(collectedResponses, responses...)
			out, collectedResponses, pendingGroups = flushFunctionResponses(out, collectedResponses, pendingGroups)
			return true
		}
		out, _ = sjson.SetRawBytes(out, "-1", []byte(content.Raw))
		callNames := functionCallNames(parts)
		if content.Get("role").String() == "model" && len(callNames) > 0 {
			pendingGroups = append(pendingGroups, &functionCallGroup{
				responsesNeeded: len(callNames),
				callNames:       callNames,
			})
		}
		return true
	})
	out, collectedResponses, pendingGroups = flushFunctionResponses(out, collectedResponses, pendingGroups)
	if len(collectedResponses) > 0 {
		out = appendFunctionResponseContent(out, collectedResponses, nil)
	}
	body, _ = sjson.SetRawBytes(body, "request.contents", out)
	return body
}

func flushFunctionResponses(out []byte, responses []gjson.Result, groups []*functionCallGroup) ([]byte, []gjson.Result, []*functionCallGroup) {
	for len(groups) > 0 && len(responses) >= groups[0].responsesNeeded {
		group := groups[0]
		groups = groups[1:]
		groupResponses := responses[:group.responsesNeeded]
		responses = responses[group.responsesNeeded:]
		out = appendFunctionResponseContent(out, groupResponses, group.callNames)
	}
	return out, responses, groups
}

func appendFunctionResponseContent(out []byte, responses []gjson.Result, callNames []string) []byte {
	content := []byte(`{"role":"user","parts":[]}`)
	for idx, response := range responses {
		raw := []byte(response.Raw)
		if strings.TrimSpace(gjson.GetBytes(raw, "functionResponse.name").String()) == "" && idx < len(callNames) {
			raw, _ = sjson.SetBytes(raw, "functionResponse.name", callNames[idx])
		}
		content, _ = sjson.SetRawBytes(content, "parts.-1", raw)
	}
	if gjson.GetBytes(content, "parts.#").Int() > 0 {
		out, _ = sjson.SetRawBytes(out, "-1", content)
	}
	return out
}

func functionResponseParts(parts gjson.Result) []gjson.Result {
	if !parts.IsArray() {
		return nil
	}
	responses := make([]gjson.Result, 0)
	parts.ForEach(func(_, part gjson.Result) bool {
		if part.Get("functionResponse").Exists() {
			responses = append(responses, part)
		}
		return true
	})
	return responses
}

func functionCallNames(parts gjson.Result) []string {
	if !parts.IsArray() {
		return nil
	}
	names := make([]string, 0)
	parts.ForEach(func(_, part gjson.Result) bool {
		if call := part.Get("functionCall"); call.Exists() {
			names = append(names, SanitizeFunctionName(call.Get("name").String()))
		}
		return true
	})
	return names
}

func filterEmptyContents(body []byte) []byte {
	contents := gjson.GetBytes(body, "request.contents")
	if !contents.IsArray() {
		return body
	}
	out := []byte(`[]`)
	removed := false
	contents.ForEach(func(_, content gjson.Result) bool {
		parts := content.Get("parts")
		if !parts.IsArray() || len(parts.Array()) == 0 {
			removed = true
			return true
		}
		out, _ = sjson.SetRawBytes(out, "-1", []byte(content.Raw))
		return true
	})
	if removed {
		body, _ = sjson.SetRawBytes(body, "request.contents", out)
	}
	return body
}

func attachDefaultSafetySettings(body []byte) []byte {
	if gjson.GetBytes(body, "request.safetySettings").Exists() {
		return body
	}
	settings := []map[string]string{
		{"category": "HARM_CATEGORY_HARASSMENT", "threshold": "OFF"},
		{"category": "HARM_CATEGORY_HATE_SPEECH", "threshold": "OFF"},
		{"category": "HARM_CATEGORY_SEXUALLY_EXPLICIT", "threshold": "OFF"},
		{"category": "HARM_CATEGORY_DANGEROUS_CONTENT", "threshold": "OFF"},
		{"category": "HARM_CATEGORY_CIVIC_INTEGRITY", "threshold": "BLOCK_NONE"},
	}
	updated, errSet := sjson.SetBytes(body, "request.safetySettings", settings)
	if errSet != nil {
		return body
	}
	return updated
}

func SanitizeFunctionName(name string) string {
	return sanitizeGeminiIdentifier(name)
}

func SanitizeFunctionID(id string) string {
	return sanitizeGeminiIdentifier(id)
}

func sanitizeGeminiIdentifier(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = functionNameSanitizer.ReplaceAllString(value, "_")
	if value == "" {
		return "_"
	}
	first := value[0]
	if !((first >= 'a' && first <= 'z') || (first >= 'A' && first <= 'Z') || first == '_') {
		if len(value) >= 64 {
			value = value[:63]
		}
		value = "_" + value
	}
	if len(value) > 64 {
		value = value[:64]
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
