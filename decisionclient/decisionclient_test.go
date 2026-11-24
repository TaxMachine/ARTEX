package decisionclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	if err := Validate(nil); err == nil {
		t.Fatal("empty question set must be rejected")
	}
	if err := Validate([]Question{{Type: "choice", Instructions: "pick"}}); err == nil {
		t.Fatal("choice without options must be rejected")
	}
	if err := Validate([]Question{{Type: "predicate"}}); err == nil {
		t.Fatal("missing instructions must be rejected")
	}
	if err := Validate([]Question{{Type: "score", Instructions: "rate"}}); err == nil {
		t.Fatal("unsupported type must be rejected")
	}
	good := []Question{
		{Type: "choice", Name: "a", Instructions: "pick", Choices: []Choice{{Value: "x"}, {Value: "y"}}},
		{Type: "predicate", Name: "b", Instructions: "is it"},
	}
	if err := Validate(good); err != nil {
		t.Fatalf("good set rejected: %v", err)
	}
}

func TestDecideWireAndParse(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/decisions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer k" {
			t.Error("bearer missing")
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"req_1","model":"m","answers":[
			{"type":"choice","name":"sev","choice":"medium","confidence":1,
			 "probabilities":[{"value":"critical","probability":0},{"value":"medium","probability":1}]},
			{"type":"predicate","name":"over","probability":0.89}],
			"usage":{"input_tokens":452,"output_tokens":0,"total_tokens":452,"cost":3.8e-05}}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, APIKey: "k", Model: "m"}
	res, err := c.Decide(context.Background(), "ctx", []Question{
		{Type: "choice", Name: "sev", Instructions: "pick", Choices: []Choice{{Value: "critical"}, {Value: "medium"}}},
		{Type: "predicate", Name: "over", Instructions: "is it"},
	})
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if gotBody["model"] != "m" || gotBody["input"] != "ctx" {
		t.Fatalf("wire body wrong: %v", gotBody)
	}
	a, ok := res.ByName("sev")
	if !ok || a.Choice != "medium" || len(a.Probabilities) != 2 {
		t.Fatalf("choice answer wrong: %+v", a)
	}
	p, _ := res.ByName("over")
	if p.Probability != 0.89 {
		t.Fatalf("predicate answer wrong: %+v", p)
	}
	if res.Usage.InputTokens != 452 {
		t.Fatalf("usage wrong: %+v", res.Usage)
	}
}

func TestDecideAnswerCountMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"r","model":"m","answers":[],"usage":{}}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Model: "m"}
	if _, err := c.Decide(context.Background(), "x", []Question{{Type: "predicate", Instructions: "q"}}); err == nil || !strings.Contains(err.Error(), "answers") {
		t.Fatalf("mismatch must error, got %v", err)
	}
}

func TestAPIErrorMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":{"message":"Use jev-1.14 on /v1/systemone","code":"model_endpoint_unsupported"}}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Model: "m"}
	_, err := c.Decide(context.Background(), "x", []Question{{Type: "predicate", Instructions: "q"}})
	if err == nil || !strings.Contains(err.Error(), "jev-1.14") {
		t.Fatalf("gateway message must surface, got %v", err)
	}
}

func TestBaseURLNormalization(t *testing.T) {
	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.URL.Path)
		_, _ = w.Write([]byte(`{"id":"r","model":"m","answers":[{"type":"predicate","probability":0.5}],"usage":{}}`))
	}))
	defer srv.Close()
	for _, base := range []string{srv.URL, srv.URL + "/", srv.URL + "/v1", srv.URL + "/v1/decisions"} {
		c := &Client{BaseURL: base, Model: "m"}
		if _, err := c.Decide(context.Background(), "x", []Question{{Type: "predicate", Instructions: "q"}}); err != nil {
			t.Fatalf("base %s: %v", base, err)
		}
	}
	for _, h := range hits {
		if h != "/v1/decisions" {
			t.Fatalf("all bases must hit /v1/decisions, saw %s", h)
		}
	}
}
