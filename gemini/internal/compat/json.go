package compat

import (
	"bytes"
	"strconv"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var dataPrefix = []byte("data:")

func WrapRequest(model string, geminiBody []byte) []byte {
	body := []byte(`{"project":"","request":{},"model":""}`)
	body = setRaw(body, "request", geminiBody)
	modelName := model
	if modelName == "" {
		modelName = gjson.GetBytes(geminiBody, "model").String()
	}
	body = setString(body, "model", modelName)
	body = deletePath(body, "request.model")
	if gjson.GetBytes(body, "request.system_instruction").Exists() {
		body = setRaw(body, "request.systemInstruction", []byte(gjson.GetBytes(body, "request.system_instruction").Raw))
		body = deletePath(body, "request.system_instruction")
	}
	return NormalizeRequest(body)
}

func UnwrapRequest(body []byte) []byte {
	request := gjson.GetBytes(body, "request")
	if !request.Exists() {
		return body
	}
	out := []byte(request.Raw)
	if model := gjson.GetBytes(body, "model").String(); model != "" {
		out = setString(out, "model", model)
	}
	if gjson.GetBytes(out, "systemInstruction").Exists() {
		out = setRaw(out, "system_instruction", []byte(gjson.GetBytes(out, "systemInstruction").Raw))
		out = deletePath(out, "systemInstruction")
	}
	return out
}

func WrapResponse(body []byte) []byte {
	return setRaw([]byte(`{"response":{}}`), "response", body)
}

func UnwrapResponse(body []byte) []byte {
	trimmed := bytes.TrimSpace(body)
	if bytes.HasPrefix(trimmed, dataPrefix) {
		trimmed = bytes.TrimSpace(trimmed[len(dataPrefix):])
	}
	if bytes.Equal(trimmed, []byte("[DONE]")) {
		return trimmed
	}
	response := gjson.GetBytes(trimmed, "response")
	if response.Exists() {
		return []byte(response.Raw)
	}
	return trimmed
}

func TokenCountJSON(count int64) []byte {
	out := make([]byte, 0, 96)
	out = append(out, `{"totalTokens":`...)
	out = strconv.AppendInt(out, count, 10)
	out = append(out, `,"promptTokensDetails":[{"modality":"TEXT","tokenCount":`...)
	out = strconv.AppendInt(out, count, 10)
	out = append(out, `}]}`...)
	return out
}

func setRaw(body []byte, path string, value []byte) []byte {
	updated, errSet := sjson.SetRawBytes(body, path, value)
	if errSet != nil {
		return body
	}
	return updated
}

func setString(body []byte, path string, value string) []byte {
	updated, errSet := sjson.SetBytes(body, path, value)
	if errSet != nil {
		return body
	}
	return updated
}

func deletePath(body []byte, path string) []byte {
	updated, errDelete := sjson.DeleteBytes(body, path)
	if errDelete != nil {
		return body
	}
	return updated
}
