package workflow

import (
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// schemaCache holds compiled step output schemas by their hash, since every
// report of a step validates against the same text.
var schemaCache sync.Map // sha256 hex -> *jsonschema.Schema

// CompileSchema compiles a JSON Schema (draft 2020-12 unless it says
// otherwise). It loads nothing: a $ref outside the document fails rather than
// reading a file or a URL, because a template is not trusted to name either.
func CompileSchema(text string) (*jsonschema.Schema, error) {
	key := HashHex(text)
	if s, ok := schemaCache.Load(key); ok {
		return s.(*jsonschema.Schema), nil
	}
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(text))
	if err != nil {
		return nil, invalidf("the output schema is not valid JSON: %v", err)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.UseLoader(jsonschema.SchemeURLLoader{})
	const loc = "urn:leapmux:workflow:output"
	if err := c.AddResource(loc, doc); err != nil {
		return nil, invalidf("the output schema cannot be read: %v", err)
	}
	s, err := c.Compile(loc)
	if err != nil {
		return nil, invalidf("the output schema does not compile: %v", err)
	}
	schemaCache.Store(key, s)
	return s, nil
}

// ValidateOutput checks a structured output against a schema text and
// returns the output's canonical form and hash.
func ValidateOutput(schemaText, doc string) (string, string, error) {
	canonical, sum, err := CanonicalJSON(doc)
	if err != nil {
		return "", "", err
	}
	if strings.TrimSpace(schemaText) == "" {
		return canonical, sum, nil
	}
	s, err := CompileSchema(schemaText)
	if err != nil {
		return "", "", err
	}
	inst, err := jsonschema.UnmarshalJSON(strings.NewReader(canonical))
	if err != nil {
		return "", "", invalidf("the structured output is not valid JSON: %v", err)
	}
	if err := s.Validate(inst); err != nil {
		return "", "", invalidf("the structured output does not match the step's schema: %v", err)
	}
	return canonical, sum, nil
}
