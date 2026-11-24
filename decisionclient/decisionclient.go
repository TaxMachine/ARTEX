// Package decisionclient speaks the OpenAI-compatible "decisions" question API
// (POST {base}/v1/decisions): one input plus up to 64 typed questions answered
// in a single call. Unlike chat completions the model emits no text — answers
// are structured, and choice answers carry a full probability distribution.
//
// Wire contract (verified against the IKHDev gateway):
//
//	{
//	  "model": "gpt-6-luna-decisions",
//	  "input": "<context the questions are about>",
//	  "questions": [
//	    {"type":"choice","name":"severity","instructions":"…",
//	     "choices":[{"value":"critical"},{"value":"high"},…]},
//	    {"type":"predicate","name":"overclaim","instructions":"<statement>"}
//	  ]
//	}
//
// Response:
//
//	{"id":"req_…","model":"…",
//	 "answers":[
//	   {"type":"choice","name":"severity","choice":"medium","confidence":1,
//	    "probabilities":[{"value":"critical","probability":0},…]},
//	   {"type":"predicate","name":"overclaim","probability":0.89}],
//	 "usage":{"input_tokens":452,"output_tokens":0,"total_tokens":452,"cost":3.8e-05}}
package decisionclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// MaxQuestions is the API's per-request question limit.
const MaxQuestions = 64

// Question is one typed question. Type is "choice" (Choices required) or
// "predicate" (a yes/no statement; the answer is its probability).
type Question struct {
	Type         string   `json:"type"`
	Name         string   `json:"name,omitempty"`
	Instructions string   `json:"instructions"`
	Choices      []Choice `json:"choices,omitempty"`
}

// Choice is one selectable value of a choice question.
type Choice struct {
	Value string `json:"value"`
}

// OptionProbability is one option's probability in a choice distribution.
type OptionProbability struct {
	Value       string  `json:"value"`
	Probability float64 `json:"probability"`
}

// Answer is the response to one question, in request order.
type Answer struct {
	Type          string              `json:"type"`
	Name          string              `json:"name"`
	Choice        string              `json:"choice,omitempty"`
	Probabilities []OptionProbability `json:"probabilities,omitempty"`
	Confidence    float64             `json:"confidence,omitempty"`
	// Probability is the predicate's answer: P(statement holds).
	Probability float64 `json:"probability,omitempty"`
}

// Usage is the request's metering block.
type Usage struct {
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	TotalTokens  int     `json:"total_tokens"`
	Cost         float64 `json:"cost"`
}

// Result is one decisions call's response.
type Result struct {
	ID      string   `json:"id"`
	Model   string   `json:"model"`
	Answers []Answer `json:"answers"`
	Usage   Usage    `json:"usage"`

	// LatencyMs is filled by the client (not part of the wire response).
	LatencyMs int `json:"-"`
}

// ByName returns the answer to the named question ("" / zero when absent).
func (r *Result) ByName(name string) (Answer, bool) {
	for _, a := range r.Answers {
		if a.Name == name {
			return a, true
		}
	}
	return Answer{}, false
}

// Client calls a decisions endpoint.
type Client struct {
	BaseURL string // e.g. https://host/v1 (the /decisions path is appended)
	APIKey  string
	Model   string
	// HTTP is the transport; nil uses http.DefaultClient.
	HTTP *http.Client
}

// Validate rejects malformed question sets before they cost a request.
func Validate(qs []Question) error {
	if len(qs) == 0 {
		return fmt.Errorf("at least one question is required")
	}
	if len(qs) > MaxQuestions {
		return fmt.Errorf("at most %d questions per request (got %d)", MaxQuestions, len(qs))
	}
	for i, q := range qs {
		if strings.TrimSpace(q.Instructions) == "" {
			return fmt.Errorf("question %d: instructions are required", i)
		}
		switch q.Type {
		case "choice":
			if len(q.Choices) < 2 {
				return fmt.Errorf("question %d (%s): a choice needs at least two choices", i, q.Name)
			}
			for _, c := range q.Choices {
				if strings.TrimSpace(c.Value) == "" {
					return fmt.Errorf("question %d (%s): choice values must not be empty", i, q.Name)
				}
			}
		case "predicate":
		default:
			return fmt.Errorf("question %d (%s): unsupported type %q (choice | predicate)", i, q.Name, q.Type)
		}
	}
	return nil
}

type wireRequest struct {
	Model     string     `json:"model"`
	Input     string     `json:"input"`
	Questions []Question `json:"questions"`
}

// Decide sends one input with its questions and returns the answers.
func (c *Client) Decide(ctx context.Context, input string, qs []Question) (*Result, error) {
	if err := Validate(qs); err != nil {
		return nil, err
	}
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	base = strings.TrimSuffix(base, "/decisions")
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	body, err := json.Marshal(wireRequest{Model: c.Model, Input: input, Questions: qs})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/decisions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	start := time.Now()
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &APIError{Status: resp.StatusCode, Body: string(raw)}
	}
	var out Result
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decisions response unparseable: %w", err)
	}
	if len(out.Answers) != len(qs) {
		return nil, fmt.Errorf("decisions returned %d answers for %d questions", len(out.Answers), len(qs))
	}
	out.LatencyMs = int(time.Since(start).Milliseconds())
	return &out, nil
}

// APIError is a non-2xx decisions response.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	// Surface the gateway's own message when it sent one: it names the
	// supported endpoint per model, which is the actionable part.
	var wire struct {
		Error struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(e.Body), &wire) == nil && wire.Error.Message != "" {
		return fmt.Sprintf("decisions API: %s (HTTP %d, code %s)", wire.Error.Message, e.Status, wire.Error.Code)
	}
	if len(e.Body) > 300 {
		e.Body = e.Body[:300]
	}
	return fmt.Sprintf("decisions API: HTTP %d: %s", e.Status, e.Body)
}

// ParseProxyURL validates a proxy URL the same way the chat stack does
// (http/https/socks5); nil when unusable.
func ParseProxyURL(raw string) *url.URL {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" {
		return nil
	}
	switch u.Scheme {
	case "http", "https", "socks5":
		return u
	}
	return nil
}
