package compat

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const GeminiSkipThoughtSignatureValidator = "skip_thought_signature_validator"

func SanitizeThoughtSignatures(payload []byte, contentsPath string) []byte {
	contentsPath = strings.TrimSpace(contentsPath)
	if contentsPath == "" {
		contentsPath = "contents"
	}
	contents := gjson.GetBytes(payload, contentsPath)
	if !contents.IsArray() {
		return payload
	}
	contents.ForEach(func(contentIdx, content gjson.Result) bool {
		isModelTurn := content.Get("role").String() == "model"
		parts := content.Get("parts")
		if !parts.IsArray() {
			return true
		}
		parts.ForEach(func(partIdx, part gjson.Result) bool {
			partPath := fmt.Sprintf("%s.%d.parts.%d", contentsPath, contentIdx.Int(), partIdx.Int())
			if part.Get("functionResponse").Exists() {
				payload = deleteThoughtSignatureFields(payload, partPath)
				return true
			}
			if !isModelTurn {
				return true
			}
			rawSignature, hasSignature := partThoughtSignature(part)
			if !part.Get("functionCall").Exists() && !part.Get("thought").Exists() && !hasSignature {
				return true
			}
			payload = deleteThoughtSignatureFields(payload, partPath)
			if strings.TrimSpace(rawSignature) == "" {
				rawSignature = GeminiSkipThoughtSignatureValidator
			}
			payload, _ = sjson.SetBytes(payload, partPath+".thoughtSignature", rawSignature)
			return true
		})
		return true
	})
	return payload
}

func partThoughtSignature(part gjson.Result) (string, bool) {
	for _, path := range []string{
		"thoughtSignature",
		"thought_signature",
		"functionCall.thoughtSignature",
		"functionCall.thought_signature",
		"functionResponse.thoughtSignature",
		"functionResponse.thought_signature",
		"extra_content.google.thought_signature",
	} {
		result := part.Get(path)
		if result.Exists() {
			return result.String(), true
		}
	}
	return "", false
}

func deleteThoughtSignatureFields(payload []byte, partPath string) []byte {
	for _, path := range []string{
		"thoughtSignature",
		"thought_signature",
		"functionCall.thoughtSignature",
		"functionCall.thought_signature",
		"functionResponse.thoughtSignature",
		"functionResponse.thought_signature",
		"extra_content.google.thought_signature",
	} {
		payload, _ = sjson.DeleteBytes(payload, partPath+"."+path)
	}
	return payload
}
