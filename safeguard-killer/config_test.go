package main

import "testing"

// hostSchemaVersionUpperBound 是生产宿主实际声明的契约上限（宿主 sdk/pluginabi.SchemaVersion = 6）。
// 宿主 registerRPCPlugin 对 resp.SchemaVersion > 该值直接拒绝注册并报
// "plugin schema version %d is not supported"，因此声明值一旦超过它插件整体不可用。
const hostSchemaVersionUpperBound uint32 = 6

// TestPluginRegistrationSchemaVersionWithinHostContract 校验注册包声明的 schema 版本与常量一致且不超宿主上限。
func TestPluginRegistrationSchemaVersionWithinHostContract(t *testing.T) {
	reg := pluginRegistration()
	if reg.SchemaVersion != schemaVersion {
		t.Fatalf("registration schema %d != declared %d", reg.SchemaVersion, schemaVersion)
	}
	if reg.SchemaVersion > hostSchemaVersionUpperBound {
		t.Fatalf("declared schema %d exceeds host contract %d", reg.SchemaVersion, hostSchemaVersionUpperBound)
	}
}
