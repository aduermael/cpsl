package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// --- fetchOpenRouterModels tests ---

func TestFetchOpenRouterModelsEmptyKey(t *testing.T) {
	models := fetchOpenRouterModels("")
	if models != nil {
		t.Errorf("expected nil for empty API key, got %d models", len(models))
	}
}

func TestFetchOpenRouterModelsSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{
					"id":             "anthropic/claude-sonnet-4-5",
					"name":           "Claude Sonnet 4.5",
					"context_length": 200000,
					"pricing": map[string]any{
						"prompt":     "0.000003",
						"completion": "0.000015",
					},
				},
				{
					"id":             "openai/gpt-4o",
					"name":           "GPT-4o",
					"context_length": 128000,
					"pricing": map[string]any{
						"prompt":     "0.0000025",
						"completion": "0.00001",
					},
				},
			},
		})
	}))
	defer srv.Close()

	models := fetchOpenRouterModelsFrom("test-key", srv.URL)
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	if models[0].Provider != ProviderOpenRouter {
		t.Errorf("Provider = %q, want %q", models[0].Provider, ProviderOpenRouter)
	}
	if models[0].ID != "anthropic/claude-sonnet-4-5" {
		t.Errorf("ID = %q, want anthropic/claude-sonnet-4-5", models[0].ID)
	}
	if models[0].ContextWindow != 200000 {
		t.Errorf("ContextWindow = %d, want 200000", models[0].ContextWindow)
	}
}

func TestFetchOpenRouterModelsPricingConversion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{
					"id":             "anthropic/claude-sonnet-4-5",
					"context_length": 200000,
					// $3.00 per million input = $0.000003 per token
					// $15.00 per million output = $0.000015 per token
					"pricing": map[string]any{
						"prompt":     "0.000003",
						"completion": "0.000015",
					},
				},
			},
		})
	}))
	defer srv.Close()

	models := fetchOpenRouterModelsFrom("test-key", srv.URL)
	if len(models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(models))
	}
	// 0.000003 * 1_000_000 = 3.0
	if models[0].PromptPrice != 3.0 {
		t.Errorf("PromptPrice = %f, want 3.0 (per million tokens)", models[0].PromptPrice)
	}
	// 0.000015 * 1_000_000 = 15.0
	if models[0].CompletionPrice != 15.0 {
		t.Errorf("CompletionPrice = %f, want 15.0 (per million tokens)", models[0].CompletionPrice)
	}
}

func TestFetchOpenRouterModelsFreeModelPricing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{
					"id":             "deepseek/deepseek-r1:free",
					"context_length": 163840,
					"pricing": map[string]any{
						"prompt":     "0",
						"completion": "0",
					},
				},
			},
		})
	}))
	defer srv.Close()

	models := fetchOpenRouterModelsFrom("test-key", srv.URL)
	if len(models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(models))
	}
	if models[0].PromptPrice != 0 {
		t.Errorf("PromptPrice = %f, want 0 for free model", models[0].PromptPrice)
	}
	if models[0].CompletionPrice != 0 {
		t.Errorf("CompletionPrice = %f, want 0 for free model", models[0].CompletionPrice)
	}
}

func TestFetchOpenRouterModelsServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	models := fetchOpenRouterModelsFrom("test-key", srv.URL)
	if models != nil {
		t.Errorf("expected nil on server error, got %d models", len(models))
	}
}

func TestFetchOpenRouterModelsInvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer srv.Close()

	models := fetchOpenRouterModelsFrom("test-key", srv.URL)
	if models != nil {
		t.Errorf("expected nil on invalid JSON, got %d models", len(models))
	}
}

func TestFetchOpenRouterModelsUnreachable(t *testing.T) {
	models := fetchOpenRouterModelsFrom("test-key", "http://127.0.0.1:1")
	if models != nil {
		t.Errorf("expected nil for unreachable server, got %d models", len(models))
	}
}

func TestFetchOpenRouterModelsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
	}))
	defer srv.Close()

	models := fetchOpenRouterModelsFrom("test-key", srv.URL)
	if len(models) != 0 {
		t.Errorf("expected 0 models for empty list, got %d", len(models))
	}
}

func TestFetchOpenRouterModelsAuthHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
	}))
	defer srv.Close()

	fetchOpenRouterModelsFrom("my-api-key", srv.URL)
	if gotAuth != "Bearer my-api-key" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer my-api-key")
	}
}

func TestFetchOpenRouterModelsInvalidPricing(t *testing.T) {
	// Non-numeric pricing strings should silently produce 0.0 (not crash).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{
					"id":             "some/model",
					"context_length": 32000,
					"pricing": map[string]any{
						"prompt":     "N/A",
						"completion": "",
					},
				},
			},
		})
	}))
	defer srv.Close()

	models := fetchOpenRouterModelsFrom("test-key", srv.URL)
	if len(models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(models))
	}
	if models[0].PromptPrice != 0 {
		t.Errorf("PromptPrice = %f, want 0 for non-numeric pricing string", models[0].PromptPrice)
	}
	if models[0].CompletionPrice != 0 {
		t.Errorf("CompletionPrice = %f, want 0 for empty pricing string", models[0].CompletionPrice)
	}
}

func TestFetchOpenRouterModelsMissingContextLength(t *testing.T) {
	// Models with no context_length field should default to 0.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{
					"id": "some/model",
					// context_length intentionally omitted
					"pricing": map[string]any{"prompt": "0", "completion": "0"},
				},
			},
		})
	}))
	defer srv.Close()

	models := fetchOpenRouterModelsFrom("test-key", srv.URL)
	if len(models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(models))
	}
	if models[0].ContextWindow != 0 {
		t.Errorf("ContextWindow = %d, want 0 when context_length is missing", models[0].ContextWindow)
	}
}

func TestFetchOpenRouterModelsRequiredHeaders(t *testing.T) {
	var gotAuth, gotReferer, gotTitle string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotReferer = r.Header.Get("HTTP-Referer")
		gotTitle = r.Header.Get("X-Title")
		json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{}})
	}))
	defer srv.Close()

	fetchOpenRouterModelsFrom("test-key", srv.URL)
	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer test-key")
	}
	if gotReferer == "" {
		t.Error("expected HTTP-Referer header to be set")
	}
	if gotTitle == "" {
		t.Error("expected X-Title header to be set")
	}
}

func TestFetchOpenRouterModelsProviderField(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "openai/gpt-4o", "context_length": 128000,
					"pricing": map[string]any{"prompt": "0.0000025", "completion": "0.00001"}},
			},
		})
	}))
	defer srv.Close()

	models := fetchOpenRouterModelsFrom("test-key", srv.URL)
	if len(models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(models))
	}
	if models[0].Provider != ProviderOpenRouter {
		t.Errorf("Provider = %q, want %q", models[0].Provider, ProviderOpenRouter)
	}
}
