package transform

import (
	"github.com/bytedance/sonic"
	"github.com/bytedance/sonic/ast"

	"aigis/internal/core"
	"aigis/internal/core/security"
)

// UnmaskTransform restores tokenized secrets in a (non-streaming) response
// body, looking up placeholders in the request's vault. It handles both
// OpenAI (choices[].message.content) and Claude (content[].text) formats.
//
// Note: streaming responses are unmasked line-by-line in the provider, not
// here, because SSE chunks are not parseable as a single JSON document.
type UnmaskTransform struct {
	scanner *security.Scanner
}

func (t *UnmaskTransform) Name() string { return TypeUnmask }

func (t *UnmaskTransform) Apply(ctx *core.AIGisContext, body []byte, _ map[string]string) ([]byte, error) {
	root, err := sonic.Get(body)
	if err != nil {
		return body, nil // Return original if parse fails
	}

	// 1. OpenAI format: choices[].message.content
	choicesNode := root.Get("choices")
	if err := choicesNode.Check(); err == nil && choicesNode.TypeSafe() == ast.V_ARRAY {
		i := 0
		for {
			choiceNode := choicesNode.Index(i)
			if err := choiceNode.Check(); err != nil {
				break
			}

			messageNode := choiceNode.Get("message")
			if err := messageNode.Check(); err != nil {
				i++
				continue
			}

			contentNode := messageNode.Get("content")
			if err := contentNode.Check(); err != nil {
				i++
				continue
			}

			if contentNode.TypeSafe() == ast.V_STRING {
				if contentStr, err := contentNode.String(); err == nil {
					unmasked := t.scanner.Unmask(ctx, contentStr)
					if unmasked != contentStr {
						messageNode.Set("content", ast.NewString(unmasked))
					}
				}
			}
			i++
		}
	}

	// 2. Claude format: content[].text (array of blocks)
	contentNode := root.Get("content")
	if err := contentNode.Check(); err == nil && contentNode.TypeSafe() == ast.V_ARRAY {
		i := 0
		for {
			blockNode := contentNode.Index(i)
			if err := blockNode.Check(); err != nil {
				break
			}

			typeNode := blockNode.Get("type")
			textNode := blockNode.Get("text")
			if typeNode.Check() == nil && textNode.Check() == nil {
				typeStr, typeErr := typeNode.String()
				textStr, textErr := textNode.String()
				if typeErr == nil && textErr == nil && typeStr == "text" {
					unmasked := t.scanner.Unmask(ctx, textStr)
					if unmasked != textStr {
						blockNode.Set("text", ast.NewString(unmasked))
					}
				}
			}
			i++
		}
	}

	return root.MarshalJSON()
}
