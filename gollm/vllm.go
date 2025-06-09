// Copyright 2025 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package gollm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	openai "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"k8s.io/klog/v2"
)

// Package-level env var storage (vllm env)
var (
	vllmAPIKey   string
	vllmEndpoint string
	vllmAPIBase  string
	vllmModel    string
)

// init reads and caches vllm environment variables:
//   - VLLM_API_KEY, VLLM_ENDPOINT, VLLM_API_BASE, VLLM_MODEL
//
// These serve as defaults; the model can be overridden by the Cobra --model flag.
// After loading env values, it registers the vllm provider factory.
func init() {
	// Load environment variables
	vllmAPIKey = os.Getenv("VLLM_API_KEY")
	vllmEndpoint = os.Getenv("VLLM_ENDPOINT")
	vllmAPIBase = "http://localhost:8000/v1" // Default base URL if not set
	vllmModel = os.Getenv("VLLM_MODEL")

	// Register only "vllm" as the provider ID
	if err := RegisterProvider("vllm", newvllmClientFactory); err != nil {
		klog.Fatalf("Failed to register vllm provider: %v", err)
	}
}

// newvllmClientFactory is the factory function for creating vllm clients.
func newvllmClientFactory(ctx context.Context, opts ClientOptions) (Client, error) {
	return NewvllmClient(ctx, opts)
}

// vllmClient implements the gollm.Client interface for vllm models.
type vllmClient struct {
	client openai.Client
}

// Ensure vllmClient implements the Client interface.
var _ Client = &vllmClient{}

// getvllmModel returns the appropriate model based on configuration and explicitly provided model name
func getvllmModel(model string) string {
	// If explicit model is provided, use it
	if model != "" {
		klog.V(2).Infof("Using explicitly provided model: %s", model)
		return model
	}

	// Check configuration
	configModel := vllmModel
	if configModel != "" {
		klog.V(1).Infof("Using model from config: %s", configModel)
		return configModel
	}

	// Default model as fallback
	klog.V(2).Info("No model specified, defaulting to gpt-4.1")
	return "ibm-granite/granite-3b-code-instruct-2k"
}

/*
NewvllmClient creates a new client for interacting with vllm.
Supports custom HTTP client (e.g., for skipping SSL verification).
*/
func NewvllmClient(ctx context.Context, opts ClientOptions) (*vllmClient, error) {
	// Get API key from loaded env var
	apiKey := vllmAPIKey
	// Do not return error if apiKey is empty; allow connection to endpoint

	// Set options for client creation
	options := []option.RequestOption{}
	if apiKey != "" {
		options = append(options, option.WithAPIKey(apiKey))
	}

	// Check for custom endpoint or API base URL
	var baseURL string
	if vllmEndpoint != "" {
		baseURL = vllmEndpoint + "/v1"
	} else {
		baseURL = vllmAPIBase // fallback to default base URL
		klog.V(2).Infof("Using default vllm API base URL: %s", baseURL)
	}
	if baseURL != "" {
		options = append(options, option.WithBaseURL(baseURL))
	}

	// Support custom HTTP client (e.g., skip SSL verification)
	httpClient := createCustomHTTPClient(opts.SkipVerifySSL)
	options = append(options, option.WithHTTPClient(httpClient))

	return &vllmClient{
		client: openai.NewClient(options...),
	}, nil
}

// Close cleans up any resources used by the client.
func (c *vllmClient) Close() error {
	// No specific cleanup needed for the vllm client currently.
	return nil
}

// StartChat starts a new chat session.
func (c *vllmClient) StartChat(systemPrompt, model string) Chat {
	// Get the model to use for this chat
	selectedModel := getvllmModel(model)

	klog.V(1).Infof("Starting new vllm chat session with model: %s", selectedModel)

	// Initialize history with system prompt if provided
	history := []openai.ChatCompletionMessageParamUnion{}
	if systemPrompt != "" {
		history = append(history, openai.SystemMessage(systemPrompt))
	}

	return &vllmChatSession{
		client:  c.client,
		history: history,
		model:   selectedModel,
		// functionDefinitions and tools will be set later via SetFunctionDefinitions
	}
}

// simpleCompletionResponse is implemented in openai.go and reused here.

// GenerateCompletion sends a completion request to the vllm API.
func (c *vllmClient) GenerateCompletion(ctx context.Context, req *CompletionRequest) (CompletionResponse, error) {
	klog.Infof("vllm GenerateCompletion called with model: %s", req.Model)
	klog.V(1).Infof("Prompt:\n%s", req.Prompt)

	// Use the Chat Completions API with the new v1.0.0 API
	completion, err := c.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model: openai.ChatModel(req.Model),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage(req.Prompt),
		},
		MaxTokens:   openai.Int(100), // Match curl: max_tokens: 100
		Temperature: openai.Float(0), // Match curl: temperature: 0
	})

	if err != nil {
		return nil, fmt.Errorf("failed to generate vllm completion: %w", err)
	}

	// Check if there are choices and a message
	if len(completion.Choices) == 0 || completion.Choices[0].Message.Content == "" {
		return nil, errors.New("received an empty response from vllm")
	}

	// Return the content of the first choice
	resp := &simpleCompletionResponse{
		content: completion.Choices[0].Message.Content,
	}

	return resp, nil
}

// SetResponseSchema is not implemented yet.
func (c *vllmClient) SetResponseSchema(schema *Schema) error {
	klog.Warning("vllmClient.SetResponseSchema is not implemented yet")
	return nil
}

// ListModels returns a slice of strings with model IDs.
// Note: This may not work with all vllm-compatible providers if they don't fully implement
// the Models.List endpoint or return data in a different format.
func (c *vllmClient) ListModels(ctx context.Context) ([]string, error) {
	res, err := c.client.Models.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("error listing models from vllm: %w", err)
	}

	modelsIDs := make([]string, 0, len(res.Data))
	for _, model := range res.Data {
		modelsIDs = append(modelsIDs, model.ID)
	}

	return modelsIDs, nil
}

// --- Chat Session Implementation ---

type vllmChatSession struct {
	client              openai.Client
	history             []openai.ChatCompletionMessageParamUnion
	model               string
	functionDefinitions []*FunctionDefinition            // Stored in gollm format
	tools               []openai.ChatCompletionToolParam // Stored in vllm format
}

// Ensure vllmChatSession implements the Chat interface.
var _ Chat = (*vllmChatSession)(nil)

// SetFunctionDefinitions stores the function definitions and converts them to vllm format.
func (cs *vllmChatSession) SetFunctionDefinitions(defs []*FunctionDefinition) error {
	cs.functionDefinitions = defs
	cs.tools = nil // Clear previous tools
	if len(defs) > 0 {
		cs.tools = make([]openai.ChatCompletionToolParam, len(defs))
		for i, gollmDef := range defs {
			// Basic conversion, assuming schema is compatible or nil
			var params openai.FunctionParameters
			if gollmDef.Parameters != nil {
				// NOTE: This assumes gollmDef.Parameters is directly marshalable to JSON
				// that fits openai.FunctionParameters. May need refinement.
				bytes, err := gollmDef.Parameters.ToRawSchema()
				if err != nil {
					return fmt.Errorf("failed to convert schema for function %s: %w", gollmDef.Name, err)
				}
				if err := json.Unmarshal(bytes, &params); err != nil {
					return fmt.Errorf("failed to unmarshal schema for function %s: %w", gollmDef.Name, err)
				}
			}
			cs.tools[i] = openai.ChatCompletionToolParam{
				Function: openai.FunctionDefinitionParam{
					Name:        gollmDef.Name,
					Description: openai.String(gollmDef.Description),
					Parameters:  params,
				},
			}
		}
		klog.Infof("[vllm] Set %d function definitions for chat session. Tool names: %v", len(cs.functionDefinitions), func() []string {
			names := make([]string, len(cs.functionDefinitions))
			for i, d := range cs.functionDefinitions {
				names[i] = d.Name
			}
			return names
		}())
	}
	klog.V(1).Infof("Set %d function definitions for vllm chat session", len(cs.functionDefinitions))
	return nil
}

// Send sends the user message(s), appends to history, and gets the LLM response.
func (cs *vllmChatSession) Send(ctx context.Context, contents ...any) (ChatResponse, error) {
	klog.V(1).InfoS("vllmChatSession.Send called", "model", cs.model, "history_len", len(cs.history))

	// Append user message(s) to history
	for _, content := range contents {
		switch c := content.(type) {
		case string:
			klog.V(2).Infof("Adding user message to history: %s", c)
			cs.history = append(cs.history, openai.UserMessage(c))
		case FunctionCallResult:
			klog.V(2).Infof("Adding tool call result to history: Name=%s, ID=%s", c.Name, c.ID)
			// Marshal the result map into a JSON string for the message content
			resultJSON, err := json.Marshal(c.Result)
			if err != nil {
				klog.Errorf("Failed to marshal function call result: %v", err)
				return nil, fmt.Errorf("failed to marshal function call result %q: %w", c.Name, err)
			}
			cs.history = append(cs.history, openai.ToolMessage(string(resultJSON), c.ID))
		default:
			// TODO: Handle other content types if necessary?
			klog.Warningf("Unhandled content type in Send: %T", content)
			return nil, fmt.Errorf("unhandled content type: %T", content)
		}
	}

	// Prepare the API request
	chatReq := openai.ChatCompletionNewParams{
		Model:       openai.ChatModel(cs.model),
		Messages:    cs.history,
		MaxTokens:   openai.Int(100), // Match curl: max_tokens: 100
		Temperature: openai.Float(0), // Match curl: temperature: 0
	}
	if len(cs.tools) > 0 {
		chatReq.Tools = cs.tools
		// chatReq.ToolChoice = openai.ToolChoiceAuto // Or specify if needed
	}

	// Call the vllm API
	klog.V(1).InfoS("Sending request to vllm Chat API", "model", cs.model, "messages", len(chatReq.Messages), "tools", len(chatReq.Tools))
	completion, err := cs.client.Chat.Completions.New(ctx, chatReq)
	if err != nil {
		// TODO: Check if error is retryable using cs.IsRetryableError
		klog.Errorf("vllm ChatCompletion API error: %v", err)
		return nil, fmt.Errorf("vllm chat completion failed: %w", err)
	}
	klog.V(1).InfoS("Received response from vllm Chat API", "id", completion.ID, "choices", len(completion.Choices))

	// Process the response
	if len(completion.Choices) == 0 {
		klog.Warning("Received response with no choices from vllm")
		return nil, errors.New("received empty response from vllm (no choices)")
	}

	// Add assistant's response (first choice) to history
	assistantMsg := completion.Choices[0].Message
	// Convert to param type before appending to history
	cs.history = append(cs.history, assistantMsg.ToParam())
	klog.V(2).InfoS("Added assistant message to history", "content_present", assistantMsg.Content != "", "tool_calls", len(assistantMsg.ToolCalls))

	// Wrap the response
	resp := &vllmChatResponse{
		openaiCompletion: completion,
	}

	return resp, nil
}

// SendStreaming sends the user message(s) and returns an iterator for the LLM response stream.
func (cs *vllmChatSession) SendStreaming(ctx context.Context, contents ...any) (ChatResponseIterator, error) {
	klog.V(1).InfoS("Starting vllm streaming request", "model", cs.model)

	// Append user message(s) to history
	for _, content := range contents {
		switch c := content.(type) {
		case string:
			klog.V(2).Infof("Adding user message to history: %s", c)
			cs.history = append(cs.history, openai.UserMessage(c))
		case FunctionCallResult:
			klog.V(2).Infof("Adding tool call result to history: Name=%s, ID=%s", c.Name, c.ID)
			resultJSON, err := json.Marshal(c.Result)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal function call result %q: %w", c.Name, err)
			}
			cs.history = append(cs.history, openai.ToolMessage(string(resultJSON), c.ID))
		default:
			return nil, fmt.Errorf("unhandled content type: %T", content)
		}
	}

	// Prepare the API request
	chatReq := openai.ChatCompletionNewParams{
		Model:       openai.ChatModel(cs.model),
		Messages:    cs.history,
		MaxTokens:   openai.Int(100), // Match curl: max_tokens: 100
		Temperature: openai.Float(0), // Match curl: temperature: 0
	}
	if len(cs.tools) > 0 {
		chatReq.Tools = cs.tools
	}

	// Start the vllm streaming request
	klog.V(1).InfoS("Sending streaming request to vllm API",
		"model", cs.model,
		"messageCount", len(chatReq.Messages),
		"toolCount", len(chatReq.Tools))

	stream := cs.client.Chat.Completions.NewStreaming(ctx, chatReq)

	// Create an accumulator to track the full response
	acc := openai.ChatCompletionAccumulator{}

	// Create and return the stream iterator
	return func(yield func(ChatResponse, error) bool) {
		defer stream.Close()

		var lastResponseChunk *vllmChatStreamResponse
		var currentContent strings.Builder
		var currentToolCalls []openai.ChatCompletionMessageToolCall

		// Process stream chunks
		for stream.Next() {
			chunk := stream.Current()

			// Update the accumulator with the new chunk
			acc.AddChunk(chunk)

			// Handle content completion
			if _, ok := acc.JustFinishedContent(); ok {
				klog.V(2).Info("Content stream finished")
			}

			// Handle refusal completion
			if refusal, ok := acc.JustFinishedRefusal(); ok {
				klog.V(2).Infof("Refusal stream finished: %v", refusal)
				yield(nil, fmt.Errorf("model refused to respond: %v", refusal))
				return
			}

			// Handle tool call completion
			if tool, ok := acc.JustFinishedToolCall(); ok {
				klog.V(2).Infof("Tool call finished: %s %s", tool.Name, tool.Arguments)
				currentToolCalls = append(currentToolCalls, openai.ChatCompletionMessageToolCall{
					ID: tool.ID,
					Function: openai.ChatCompletionMessageToolCallFunction{
						Name:      tool.Name,
						Arguments: tool.Arguments,
					},
				})
			}

			// Create a streaming response with proper nil checks
			streamResponse := &vllmChatStreamResponse{
				streamChunk: chunk,
				accumulator: acc,
				content:     "", // Default to empty content
				toolCalls:   currentToolCalls,
			}

			// Only process content if there are choices and a delta
			if len(chunk.Choices) > 0 {
				delta := chunk.Choices[0].Delta
				if delta.Content != "" {
					currentContent.WriteString(delta.Content)
					streamResponse.content = delta.Content // Only set content if there's new content
				}
			}

			// Keep track of the last response for history
			lastResponseChunk = &vllmChatStreamResponse{
				streamChunk: chunk,
				accumulator: acc,
				content:     currentContent.String(), // Full accumulated content for history
				toolCalls:   currentToolCalls,
			}

			// Only yield if there's actual content or tool calls to report
			if streamResponse.content != "" || len(streamResponse.toolCalls) > 0 {
				if !yield(streamResponse, nil) {
					return
				}
			}
		}

		// Check for errors after streaming completes
		if err := stream.Err(); err != nil {
			klog.Errorf("Error in vllm streaming: %v", err)
			yield(nil, fmt.Errorf("vllm streaming error: %w", err))
			return
		}

		// Update conversation history with the complete message
		if lastResponseChunk != nil {
			completeMessage := openai.ChatCompletionMessage{
				Content:   currentContent.String(),
				Role:      "assistant",
				ToolCalls: currentToolCalls,
			}

			// Append the full assistant response to history
			cs.history = append(cs.history, completeMessage.ToParam())
			klog.V(2).InfoS("Added complete assistant message to history",
				"content_present", completeMessage.Content != "",
				"tool_calls", len(completeMessage.ToolCalls))
		}
	}, nil
}

// IsRetryableError determines if an error from the vllm API should be retried.
func (cs *vllmChatSession) IsRetryableError(err error) bool {
	if err == nil {
		return false
	}
	return DefaultIsRetryableError(err)
}

// --- Helper structs for ChatResponse interface ---

type vllmChatResponse struct {
	openaiCompletion *openai.ChatCompletion
}

var _ ChatResponse = (*vllmChatResponse)(nil)

func (r *vllmChatResponse) UsageMetadata() any {
	// Check if the main completion object and Usage exist
	if r.openaiCompletion != nil && r.openaiCompletion.Usage.TotalTokens > 0 { // Check a field within Usage
		return r.openaiCompletion.Usage
	}
	return nil
}

func (r *vllmChatResponse) Candidates() []Candidate {
	if r.openaiCompletion == nil {
		return nil
	}
	candidates := make([]Candidate, len(r.openaiCompletion.Choices))
	for i, choice := range r.openaiCompletion.Choices {
		candidates[i] = &vllmCandidate{openaiChoice: &choice}
	}
	return candidates
}

type vllmCandidate struct {
	openaiChoice *openai.ChatCompletionChoice
}

var _ Candidate = (*vllmCandidate)(nil)

func (c *vllmCandidate) Parts() []Part {
	// Check if the choice exists before accessing Message
	if c.openaiChoice == nil {
		return nil
	}

	// vllm message can have Content AND ToolCalls
	var parts []Part
	if c.openaiChoice.Message.Content != "" {
		parts = append(parts, &vllmPart{content: c.openaiChoice.Message.Content})
	}
	if len(c.openaiChoice.Message.ToolCalls) > 0 {
		parts = append(parts, &vllmPart{toolCalls: c.openaiChoice.Message.ToolCalls})
	}
	return parts
}

// String provides a simple string representation for logging/debugging.
func (c *vllmCandidate) String() string {
	if c.openaiChoice == nil {
		return "<nil candidate>"
	}
	content := "<no content>"
	if c.openaiChoice.Message.Content != "" {
		content = c.openaiChoice.Message.Content
	}
	toolCalls := len(c.openaiChoice.Message.ToolCalls)
	finishReason := string(c.openaiChoice.FinishReason)
	return fmt.Sprintf("Candidate(FinishReason: %s, ToolCalls: %d, Content: %q)", finishReason, toolCalls, content)
}

type vllmPart struct {
	content   string
	toolCalls []openai.ChatCompletionMessageToolCall // Correct type
}

var _ Part = (*vllmPart)(nil)

func (p *vllmPart) AsText() (string, bool) {
	return p.content, p.content != ""
}

func (p *vllmPart) AsFunctionCalls() ([]FunctionCall, bool) {
	if len(p.toolCalls) == 0 {
		return nil, false
	}

	// Convert only complete function calls
	var completeCalls []FunctionCall
	for _, tc := range p.toolCalls {
		// Check if it's a function call by seeing if Function Name is populated
		if tc.Function.Name == "" { // Adjusted check for function calls
			klog.V(2).Infof("Skipping non-function tool call ID: %s", tc.ID)
			continue
		}
		var args map[string]any
		// Attempt to unmarshal arguments, ignore error for now if it fails
		_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)

		completeCalls = append(completeCalls, FunctionCall{
			ID:        tc.ID, // Pass the Tool Call ID
			Name:      tc.Function.Name,
			Arguments: args,
		})
	}
	return completeCalls, true
}

// Update vllmChatStreamResponse to include accumulated content
type vllmChatStreamResponse struct {
	streamChunk openai.ChatCompletionChunk
	accumulator openai.ChatCompletionAccumulator
	content     string
	toolCalls   []openai.ChatCompletionMessageToolCall
}

// Update Candidates() to use accumulated content
func (r *vllmChatStreamResponse) Candidates() []Candidate {
	if len(r.streamChunk.Choices) == 0 {
		return nil
	}

	candidates := make([]Candidate, len(r.streamChunk.Choices))
	for i, choice := range r.streamChunk.Choices {
		candidates[i] = &vllmStreamCandidate{
			streamChoice: choice,
			content:      r.content,
			toolCalls:    r.toolCalls,
		}
	}
	return candidates
}

// Update vllmStreamCandidate to handle delta content
type vllmStreamCandidate struct {
	streamChoice openai.ChatCompletionChunkChoice
	content      string // This will now be just the delta content
	toolCalls    []openai.ChatCompletionMessageToolCall
}

// Update Parts() to handle delta content
func (c *vllmStreamCandidate) Parts() []Part {
	var parts []Part

	// Only include the delta content
	if c.content != "" {
		parts = append(parts, &vllmStreamPart{
			content: c.content,
		})
	}

	// Include accumulated tool calls
	if len(c.toolCalls) > 0 {
		parts = append(parts, &vllmStreamPart{
			toolCalls: c.toolCalls,
		})
	}

	return parts
}

// Add UsageMetadata implementation
func (r *vllmChatStreamResponse) UsageMetadata() any {
	if r.accumulator.Usage.TotalTokens > 0 {
		return r.accumulator.Usage
	}
	return nil
}

// Add String implementation
func (c *vllmStreamCandidate) String() string {
	return fmt.Sprintf("StreamingCandidate(Content: %q, ToolCalls: %d)",
		c.content, len(c.toolCalls))
}

// Define vllmStreamPart
type vllmStreamPart struct {
	content   string
	toolCalls []openai.ChatCompletionMessageToolCall
}

// Ensure vllmStreamPart implements Part interface
var _ Part = (*vllmStreamPart)(nil)

func (p *vllmStreamPart) AsText() (string, bool) {
	return p.content, p.content != ""
}

func (p *vllmStreamPart) AsFunctionCalls() ([]FunctionCall, bool) {
	if len(p.toolCalls) == 0 {
		return nil, false
	}

	calls := make([]FunctionCall, 0, len(p.toolCalls))
	for _, tc := range p.toolCalls {
		var args map[string]any
		if tc.Function.Arguments != "" {
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
				klog.V(2).Infof("Error unmarshalling function arguments: %v", err)
				args = make(map[string]any)
			}
		} else {
			args = make(map[string]any)
		}

		calls = append(calls, FunctionCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: args,
		})
	}
	return calls, true
}
