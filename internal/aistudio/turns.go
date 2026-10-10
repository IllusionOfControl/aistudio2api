package aistudio

import "strings"

// assistantPrefixInstruction is the system instruction appended when rewriting trailing assistant turn into continuation prefix
const assistantPrefixInstruction = "Continue the assistant response after the following prefix. Return only the continuation, without repeating the prefix."

// normalizeTurns rewrites trailing consecutive assistant turns into continuation prefixes, using system prompt as user turn when conversation is empty
func normalizeTurns(system string, contents []Content) (string, []Content) {
	tail := len(contents)
	for tail > 0 && contents[tail-1].Role == RoleAssistant {
		tail--
	}
	if prefix := assistantPrefix(contents[tail:]); strings.TrimSpace(prefix) != "" {
		instruction := assistantPrefixInstruction + "\n<assistant_prefix>" + prefix + "</assistant_prefix>"
		if system != "" {
			instruction = system + "\n" + instruction
		}
		system = instruction
	}
	contents = contents[:tail]
	if len(contents) == 0 && strings.TrimSpace(system) != "" {
		return "", []Content{{Role: RoleUser, Parts: []Part{{Text: system}}}}
	}
	return system, contents
}

// assistantPrefix concatenates text and code execution contents of assistant turns in order
func assistantPrefix(contents []Content) string {
	var prefix strings.Builder
	block := func(text string) {
		if prefix.Len() > 0 && !strings.HasSuffix(prefix.String(), "\n") {
			prefix.WriteString("\n")
		}
		prefix.WriteString(text + "\n")
	}
	for _, content := range contents {
		for _, part := range content.Parts {
			switch {
			case part.Thought:
			case part.Text != "":
				prefix.WriteString(part.Text)
			case part.ExecutableCode != nil:
				block("```" + strings.ToLower(part.ExecutableCode.Language) + "\n" + strings.TrimSuffix(part.ExecutableCode.Code, "\n") + "\n```")
			case part.CodeExecutionResult != nil:
				block("```text\n" + strings.TrimSuffix(part.CodeExecutionResult.Output+part.CodeExecutionResult.Error, "\n") + "\n```")
			}
		}
	}
	return prefix.String()
}
