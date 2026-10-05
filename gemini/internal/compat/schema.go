package compat

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var gjsonPathKeyReplacer = strings.NewReplacer(".", "\\.", "*", "\\*", "?", "\\?")

func CleanJSONSchemaForGemini(schema []byte) []byte {
	if len(schema) == 0 {
		return []byte(`{"type":"object","properties":{}}`)
	}
	jsonStr := string(schema)
	jsonStr = convertConstToEnum(jsonStr)
	jsonStr = convertEnumValuesToStrings(jsonStr)
	jsonStr = flattenTypeArrays(jsonStr)
	jsonStr = removeUnsupportedSchemaKeywords(jsonStr)
	jsonStr = cleanupRequiredFields(jsonStr)
	if !gjson.Valid(jsonStr) {
		return schema
	}
	return []byte(jsonStr)
}

func convertConstToEnum(jsonStr string) string {
	for _, path := range findSchemaPaths(jsonStr, "const") {
		value := gjson.Get(jsonStr, path)
		if !value.Exists() {
			continue
		}
		enumPath := trimPathSuffix(path, ".const") + ".enum"
		if !gjson.Get(jsonStr, enumPath).Exists() {
			updated, _ := sjson.SetBytes([]byte(jsonStr), enumPath, []any{value.Value()})
			jsonStr = string(updated)
		}
	}
	return jsonStr
}

func convertEnumValuesToStrings(jsonStr string) string {
	for _, path := range findSchemaPaths(jsonStr, "enum") {
		enumValue := gjson.Get(jsonStr, path)
		if !enumValue.IsArray() {
			continue
		}
		values := make([]string, 0, len(enumValue.Array()))
		for _, item := range enumValue.Array() {
			values = append(values, item.String())
		}
		updated, _ := sjson.SetBytes([]byte(jsonStr), path, values)
		jsonStr = string(updated)
		parentPath := trimPathSuffix(path, ".enum")
		updated, _ = sjson.SetBytes([]byte(jsonStr), joinSchemaPath(parentPath, "type"), "string")
		jsonStr = string(updated)
	}
	return jsonStr
}

func flattenTypeArrays(jsonStr string) string {
	for _, path := range findSchemaPaths(jsonStr, "type") {
		typeValue := gjson.Get(jsonStr, path)
		if !typeValue.IsArray() || len(typeValue.Array()) == 0 {
			continue
		}
		types := make([]string, 0, len(typeValue.Array()))
		for _, item := range typeValue.Array() {
			if item.String() != "null" && item.String() != "" {
				types = append(types, item.String())
			}
		}
		selected := "string"
		if len(types) > 0 {
			selected = types[0]
		}
		updated, _ := sjson.SetBytes([]byte(jsonStr), path, selected)
		jsonStr = string(updated)
		if len(types) > 1 {
			jsonStr = appendSchemaHint(jsonStr, trimPathSuffix(path, ".type"), "Accepts: "+strings.Join(types, " | "))
		}
	}
	return jsonStr
}

func removeUnsupportedSchemaKeywords(jsonStr string) string {
	keywords := []string{
		"$schema", "$defs", "definitions", "const", "$ref", "$id", "$comment",
		"additionalProperties", "propertyNames", "patternProperties",
		"minLength", "maxLength", "exclusiveMinimum", "exclusiveMaximum",
		"pattern", "minItems", "maxItems", "uniqueItems", "format",
		"default", "examples", "nullable", "title", "enumDescriptions",
		"enumTitles", "prefill", "deprecated",
	}
	deletePaths := make([]string, 0)
	for _, path := range findSchemaPathsByFields(jsonStr, keywords) {
		if isSchemaPropertyDefinition(trimPathSuffixByField(path)) {
			continue
		}
		deletePaths = append(deletePaths, path)
	}
	deletePaths = append(deletePaths, findExtensionFieldPaths(jsonStr)...)
	sortBySchemaDepth(deletePaths)
	out := []byte(jsonStr)
	for _, path := range deletePaths {
		out, _ = sjson.DeleteBytes(out, path)
	}
	return string(out)
}

func cleanupRequiredFields(jsonStr string) string {
	for _, path := range findSchemaPaths(jsonStr, "required") {
		parentPath := trimPathSuffix(path, ".required")
		propertiesPath := joinSchemaPath(parentPath, "properties")
		required := gjson.Get(jsonStr, path)
		properties := gjson.Get(jsonStr, propertiesPath)
		if !required.IsArray() || !properties.IsObject() {
			continue
		}
		valid := make([]string, 0, len(required.Array()))
		for _, item := range required.Array() {
			key := item.String()
			if properties.Get(escapeSchemaPathKey(key)).Exists() {
				valid = append(valid, key)
			}
		}
		if len(valid) == len(required.Array()) {
			continue
		}
		if len(valid) == 0 {
			jsonStr, _ = sjson.Delete(jsonStr, path)
		} else {
			updated, _ := sjson.SetBytes([]byte(jsonStr), path, valid)
			jsonStr = string(updated)
		}
	}
	return jsonStr
}

func findSchemaPaths(jsonStr string, field string) []string {
	var paths []string
	walkSchemaForField(gjson.Parse(jsonStr), "", field, &paths)
	return paths
}

func findSchemaPathsByFields(jsonStr string, fields []string) []string {
	fieldSet := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		fieldSet[field] = struct{}{}
	}
	paths := make([]string, 0)
	walkSchemaForFields(gjson.Parse(jsonStr), "", fieldSet, &paths)
	return paths
}

func walkSchemaForField(value gjson.Result, path string, field string, paths *[]string) {
	walkSchema(value, path, func(childPath string, key string) {
		if key == field {
			*paths = append(*paths, childPath)
		}
	})
}

func walkSchemaForFields(value gjson.Result, path string, fields map[string]struct{}, paths *[]string) {
	walkSchema(value, path, func(childPath string, key string) {
		if _, ok := fields[key]; ok {
			*paths = append(*paths, childPath)
		}
	})
}

func walkSchema(value gjson.Result, path string, visit func(childPath string, key string)) {
	if value.IsArray() {
		items := value.Array()
		for i := len(items) - 1; i >= 0; i-- {
			walkSchema(items[i], joinSchemaPath(path, strconv.Itoa(i)), visit)
		}
		return
	}
	if !value.IsObject() {
		return
	}
	value.ForEach(func(key, val gjson.Result) bool {
		keyStr := key.String()
		childPath := joinSchemaPath(path, escapeSchemaPathKey(keyStr))
		visit(childPath, keyStr)
		walkSchema(val, childPath, visit)
		return true
	})
}

func findExtensionFieldPaths(jsonStr string) []string {
	paths := make([]string, 0)
	walkSchema(gjson.Parse(jsonStr), "", func(path string, key string) {
		if strings.HasPrefix(key, "x-") && !isSchemaPropertyDefinition(trimPathSuffix(path, "."+escapeSchemaPathKey(key))) {
			paths = append(paths, path)
		}
	})
	return paths
}

func sortBySchemaDepth(paths []string) {
	sort.Slice(paths, func(i, j int) bool { return len(paths[i]) > len(paths[j]) })
}

func trimPathSuffixByField(path string) string {
	idx := strings.LastIndex(path, ".")
	if idx < 0 {
		return ""
	}
	return path[:idx]
}

func trimPathSuffix(path string, suffix string) string {
	if path == strings.TrimPrefix(suffix, ".") {
		return ""
	}
	return strings.TrimSuffix(path, suffix)
}

func joinSchemaPath(base string, suffix string) string {
	if base == "" {
		return suffix
	}
	return base + "." + suffix
}

func appendSchemaHint(jsonStr string, parentPath string, hint string) string {
	descPath := joinSchemaPath(parentPath, "description")
	if parentPath == "" {
		descPath = "description"
	}
	existing := gjson.Get(jsonStr, descPath).String()
	if existing != "" {
		hint = fmt.Sprintf("%s (%s)", existing, hint)
	}
	updated, _ := sjson.SetBytes([]byte(jsonStr), descPath, hint)
	return string(updated)
}

func isSchemaPropertyDefinition(path string) bool {
	return path == "properties" || strings.HasSuffix(path, ".properties")
}

func escapeSchemaPathKey(key string) string {
	if strings.IndexAny(key, ".*?") == -1 {
		return key
	}
	return gjsonPathKeyReplacer.Replace(key)
}
