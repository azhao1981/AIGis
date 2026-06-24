package transform

import (
	"fmt"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"aigis/internal/core"
)

// FieldMapTransform maps fields from source paths to target paths using
// gjson (read) and sjson (write). Config format: "target_path": "source_path".
type FieldMapTransform struct{}

func (t *FieldMapTransform) Name() string { return TypeFieldMap }

func (t *FieldMapTransform) Apply(_ *core.AIGisContext, body []byte, config map[string]string) ([]byte, error) {
	result := body

	for targetPath, sourcePath := range config {
		value := gjson.GetBytes(body, sourcePath)
		if !value.Exists() {
			continue
		}

		var err error
		switch value.Type {
		case gjson.String:
			result, err = sjson.SetBytes(result, targetPath, value.String())
		case gjson.Number:
			result, err = sjson.SetBytes(result, targetPath, value.Float())
		case gjson.True, gjson.False:
			result, err = sjson.SetBytes(result, targetPath, value.Bool())
		default:
			// Objects/arrays: set as raw JSON
			result, err = sjson.SetRawBytes(result, targetPath, []byte(value.Raw))
		}

		if err != nil {
			return nil, fmt.Errorf("failed to set field %s: %w", targetPath, err)
		}
	}

	return result, nil
}
