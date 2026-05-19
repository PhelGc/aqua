// Package llm define los tipos puros del protocolo OpenAI chat-completions
// que usan el agente y los workers. Sin lógica, solo wire types.
package llm

type Message struct {
	Role string `json:"role"`
	// Content NO usa omitempty: algunos providers (DeepSeek) requieren el
	// campo presente aunque sea string vacío, en mensajes de assistant que
	// solo emiten tool_calls. OpenAI y compatibles aceptan el campo vacío
	// sin problema, así que es el común denominador seguro.
	Content          string     `json:"content"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
}

type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type Tool struct {
	Type     string   `json:"type"`
	Function ToolFunc `json:"function"`
}

type ToolFunc struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
}

type ChatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Tools    []Tool    `json:"tools,omitempty"`
	Stream   bool      `json:"stream,omitempty"`
}

type ChatResponse struct {
	Choices []struct {
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// StreamChunk es un evento individual del stream SSE de /chat/completions.
// Cada uno trae deltas incrementales del mensaje en construcción.
type StreamChunk struct {
	Choices []struct {
		Delta        StreamDelta `json:"delta"`
		FinishReason string      `json:"finish_reason,omitempty"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// StreamDelta es el incremento de un chunk: texto nuevo del content, del
// reasoning, o de tool-calls. El role solo viene en el primer chunk.
type StreamDelta struct {
	Role             string                `json:"role,omitempty"`
	Content          string                `json:"content,omitempty"`
	ReasoningContent string                `json:"reasoning_content,omitempty"`
	ToolCalls        []StreamToolCallDelta `json:"tool_calls,omitempty"`
}

// StreamToolCallDelta es un fragmento de un tool-call. El primer chunk de
// cada tool-call trae index+id+function.name; los siguientes incrementan
// function.arguments. El caller agrupa por Index.
type StreamToolCallDelta struct {
	Index    int                       `json:"index"`
	ID       string                    `json:"id,omitempty"`
	Type     string                    `json:"type,omitempty"`
	Function StreamToolCallFuncDelta   `json:"function,omitempty"`
}

type StreamToolCallFuncDelta struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}
