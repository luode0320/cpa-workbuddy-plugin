package cursorproto

import (
	"slices"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const queryFieldLimit = 8

// QueryShape deliberately excludes IDs and all field values, including nested messages.
type QueryShape struct {
	Fields    []protoreflect.FieldNumber
	Truncated bool
}

func queryShape(query protoreflect.Message) QueryShape {
	var shape QueryShape
	query.Range(func(field protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
		if len(shape.Fields) == queryFieldLimit {
			shape.Truncated = true
			return false
		}
		shape.Fields = append(shape.Fields, field.Number())
		return true
	})
	unknown := query.GetUnknown()
	for len(unknown) > 0 {
		if len(shape.Fields) == queryFieldLimit {
			shape.Truncated = true
			break
		}
		number, _, size := protowire.ConsumeField(unknown)
		if size < 0 {
			// Diagnostics must not change a previously decoded event's outcome.
			shape.Truncated = true
			break
		}
		shape.Fields = append(shape.Fields, number)
		unknown = unknown[size:]
	}
	slices.Sort(shape.Fields)
	return shape
}
