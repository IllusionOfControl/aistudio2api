package aistudio

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/dlclark/regexp2/v2"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// toolContract stores call mode and argument contracts explicitly requested by client
type toolContract struct {
	required   bool
	parallel   *bool
	names      map[string]bool
	schemas    map[string]*jsonschema.Schema
	serverSide bool
}

// prepareToolRequest converts tool choices and constraints into a joint generation request
func prepareToolRequest(request GenerateRequest) (GenerateRequest, *toolContract, error) {
	config := request.Tools.ToolConfig
	if config.Mode == "none" {
		return request, nil, nil
	}
	if search := request.Tools.GoogleSearch; search != nil {
		if search.ContextSize != "" {
			request.System += "\nUse " + search.ContextSize + " search context depth."
		}
		if len(search.UserLocation) > 0 {
			request.System += "\nApproximate search user location: " + string(search.UserLocation)
		}
		if len(search.AllowedDomains) > 0 {
			request.System += "\nRestrict web searches to these domains using site: queries: " + strings.Join(search.AllowedDomains, ", ")
		}
	}
	contract := &toolContract{required: config.Mode == "required", parallel: config.ParallelCalls, names: map[string]bool{}, schemas: map[string]*jsonschema.Schema{}}
	if contract.parallel != nil && *contract.parallel || len(request.Tools.Functions) == 0 {
		contract.parallel = nil
	}
	functions := make([]FunctionDeclaration, 0, len(request.Tools.Functions))
	for _, declaration := range request.Tools.Functions {
		if len(config.AllowedFunctionNames) > 0 && !slices.Contains(config.AllowedFunctionNames, declaration.Name) {
			continue
		}
		if contract.names[declaration.Name] {
			return request, nil, fmt.Errorf("duplicate function %q", declaration.Name)
		}
		if config.Mode == "validated" {
			declaration.Strict = true
		}
		contract.names[declaration.Name] = true
		functions = append(functions, declaration)
		if !declaration.Strict && config.Mode != "validated" {
			continue
		}
		raw, err := normalizeFunctionParameters(declaration.Parameters)
		if err != nil {
			return request, nil, err
		}
		document, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			return request, nil, err
		}
		document = validationSchema(document)
		compiler := jsonschema.NewCompiler()
		compiler.UseLoader(toolSchemaLoader{})
		compiler.UseRegexpEngine(compileToolPattern)
		const resource = "https://tools.invalid/parameters.json"
		if err := compiler.AddResource(resource, document); err != nil {
			return request, nil, err
		}
		if schema, err := compiler.Compile(resource); err == nil {
			contract.schemas[declaration.Name] = schema
		}
	}
	for _, name := range config.AllowedFunctionNames {
		if !contract.names[name] {
			return request, nil, fmt.Errorf("tool choice referenced undeclared function %q", name)
		}
	}
	contract.serverSide = len(config.AllowedFunctionNames) == 0 && (len(request.Tools.Google) > 0 || request.Tools.GoogleSearch != nil)
	if contract.required && len(functions) == 0 && !contract.serverSide {
		return request, nil, fmt.Errorf("required tool choice requires at least one tool")
	}
	if len(config.AllowedFunctionNames) > 0 || config.Mode == "validated" {
		request.Tools.Functions = functions
	}
	if contract.required {
		names := make([]string, 0, len(functions))
		for _, declaration := range functions {
			names = append(names, declaration.Name)
		}
		if contract.serverSide {
			names = append(names, request.Tools.Google...)
			if request.Tools.GoogleSearch != nil && !slices.Contains(names, "google_search") {
				names = append(names, "google_search")
			}
		}
		request.System += "\nUse one of these tools in this response: " + strings.Join(names, ", ") + ". Use the tool before answering."
	}
	if contract.parallel != nil {
		request.System += "\nCall at most one function in this response."
	}
	if !contract.required && len(config.AllowedFunctionNames) == 0 && len(contract.schemas) == 0 && contract.parallel == nil {
		contract = nil
	}
	return request, contract, nil
}

// toolPattern validates JSON Schema strings using ECMA regular expressions
type toolPattern struct {
	*regexp2.Regexp
}

// MatchString returns whether the string satisfies the parameter pattern
func (pattern toolPattern) MatchString(value string) bool {
	matched, err := pattern.Regexp.MatchString(value)
	return err == nil && matched
}

// compileToolPattern compiles an ECMA pattern for JSON Schema
func compileToolPattern(value string) (jsonschema.Regexp, error) {
	pattern, err := regexp2.Compile(value, regexp2.ECMAScript)
	if err != nil {
		return nil, err
	}
	pattern.MatchTimeout = time.Second
	return toolPattern{pattern}, nil
}

// toolSchemaLoader limits reference resolution to Schema resources provided in the request
type toolSchemaLoader struct{}

// Load returns an error for external Schema references not provided with the request
func (toolSchemaLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("schema reference %q not included in tool declarations", url)
}

// validationSchema converts Gemini type names and nullable into JSON Schema validation structures
func validationSchema(value any) any {
	schema, ok := value.(map[string]any)
	if !ok {
		return value
	}
	if name, ok := schema["type"].(string); ok {
		schema["type"] = strings.ToLower(name)
	}
	if names, ok := schema["type"].([]any); ok {
		for index, name := range names {
			if name, ok := name.(string); ok {
				names[index] = strings.ToLower(name)
			}
		}
	}
	for _, key := range []string{"properties", "patternProperties", "$defs", "definitions", "dependentSchemas"} {
		if entries, ok := schema[key].(map[string]any); ok {
			for name, child := range entries {
				entries[name] = validationSchema(child)
			}
		}
	}
	for _, key := range []string{"items", "not", "additionalProperties", "contains", "if", "then", "else", "unevaluatedProperties", "unevaluatedItems"} {
		if child, ok := schema[key]; ok {
			schema[key] = validationSchema(child)
		}
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf", "prefixItems"} {
		if entries, ok := schema[key].([]any); ok {
			for index, child := range entries {
				entries[index] = validationSchema(child)
			}
		}
	}
	if schema["nullable"] == true {
		delete(schema, "nullable")
		return map[string]any{"anyOf": []any{schema, map[string]any{"type": "null"}}}
	}
	return schema
}

// forward verifies explicit contracts before complete tool events are sent to client
func (contract *toolContract) forward(ctx context.Context, source <-chan Event) <-chan Event {
	if contract == nil {
		return source
	}
	destination := make(chan Event, 8)
	go func() {
		defer close(destination)
		calls := 0
		serverSideUsed := false
		failed := false
		for event := range source {
			if failed {
				continue
			}
			var err error
			if event.Kind == EventGrounding || event.Kind == EventExecutableCode || event.Kind == EventCodeExecutionResult {
				serverSideUsed = true
			}
			if event.Kind == EventToolCall && event.ToolCall != nil {
				calls++
				call := event.ToolCall
				switch {
				case !contract.names[call.Name]:
					err = fmt.Errorf("upstream returned unselected function %q", call.Name)
				case contract.parallel != nil && !*contract.parallel && calls > 1:
					err = fmt.Errorf("upstream violated single tool call constraint")
				default:
					if schema := contract.schemas[call.Name]; schema != nil {
						value, decodeErr := jsonschema.UnmarshalJSON(bytes.NewReader(call.Arguments))
						if decodeErr != nil {
							err = decodeErr
						} else {
							err = schema.Validate(value)
						}
						if err != nil {
							err = fmt.Errorf("upstream function %q arguments violated strict schema: %w", call.Name, err)
						}
					}
				}
			}
			if event.Kind == EventFinish && contract.required && calls == 0 && !(contract.serverSide && serverSideUsed) {
				err = fmt.Errorf("upstream did not return required tool call")
			}
			if err != nil {
				event = Event{Kind: EventError, Err: err}
				failed = true
			}
			select {
			case destination <- event:
			case <-ctx.Done():
				return
			}
		}
	}()
	return destination
}
