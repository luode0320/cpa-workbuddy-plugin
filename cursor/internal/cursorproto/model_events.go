package cursorproto

import (
	"fmt"
	"google.golang.org/protobuf/proto"
)

func DecodeModels(raw []byte) ([]string, error) {
	response, err := newMessage("GetUsableModelsResponse")
	if err != nil {
		return nil, err
	}
	if err := proto.Unmarshal(raw, response); err != nil {
		return nil, fmt.Errorf("decode Cursor models response: %w", err)
	}
	modelsField, err := requireField(response, "models")
	if err != nil {
		return nil, err
	}
	models := response.Get(modelsField).List()
	ids := make([]string, 0, models.Len())
	for index := range models.Len() {
		model := models.Get(index).Message()
		idField, err := requireField(model, "model_id")
		if err != nil {
			return nil, err
		}
		if id := model.Get(idField).String(); id != "" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}
