package transform

import (
	"bytes"
	"fmt"
	"text/template"

	"github.com/bytedance/sonic"

	"aigis/internal/core"
)

// TemplateTransform reconstructs the body using a Go text/template, enabling
// cross-provider protocol translation (e.g. OpenAI -> Dify). The rendered
// output must be valid JSON. Config key "template" holds the template string.
//
// The template gets a "json" function that marshals any value to a JSON literal
// (with surrounding quotes for strings). Use it for any field that carries
// arbitrary user text — e.g. {"query": {{json (index .messages 0 "content")}}}
// — so embedded quotes/newlines stay valid JSON instead of breaking the output.
type TemplateTransform struct{}

func (t *TemplateTransform) Name() string { return TypeTemplate }

// templateFuncs are the helpers exposed to every template.
var templateFuncs = template.FuncMap{
	"json": func(v interface{}) (string, error) {
		b, err := sonic.Marshal(v)
		if err != nil {
			return "", fmt.Errorf("json func: %w", err)
		}
		return string(b), nil
	},
}

func (t *TemplateTransform) Apply(_ *core.AIGisContext, body []byte, config map[string]string) ([]byte, error) {
	tmplStr := config["template"]
	if tmplStr == "" {
		return body, nil
	}

	var data map[string]interface{}
	if err := sonic.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("failed to parse body for template: %w", err)
	}

	tmpl, err := template.New("transform").Funcs(templateFuncs).Parse(tmplStr)
	if err != nil {
		return nil, fmt.Errorf("invalid template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("template execution failed: %w", err)
	}

	result := buf.Bytes()
	if !sonic.Valid(result) {
		return nil, fmt.Errorf("template output is not valid JSON")
	}

	return result, nil
}
