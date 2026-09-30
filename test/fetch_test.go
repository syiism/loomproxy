package test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"loomproxy/base"
)

func TestFetchJSONWithPooledBuffer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"code":0,"data":{"name":"测试"}}`))
	}))
	defer srv.Close()

	h := base.NewBaseHandler()
	// 连续多次请求，验证池化 buffer 复用后结果仍正确
	for i := 0; i < 5; i++ {
		data, err := h.FetchJSON(context.Background(), srv.URL, nil)
		if err != nil {
			t.Fatalf("FetchJSON failed: %v", err)
		}
		inner, ok := data["data"].(map[string]interface{})
		if !ok || inner["name"] != "测试" {
			t.Fatalf("unexpected result: %v", data)
		}
	}
}

func TestFetchTextWithPooledBuffer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html><body>hello</body></html>"))
	}))
	defer srv.Close()

	h := base.NewBaseHandler()
	for i := 0; i < 5; i++ {
		text, err := h.FetchText(context.Background(), srv.URL, nil)
		if err != nil {
			t.Fatalf("FetchText failed: %v", err)
		}
		if !strings.Contains(text, "hello") {
			t.Fatalf("unexpected text: %s", text)
		}
	}
}

func TestFetchJSONUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	h := base.NewBaseHandler()
	_, err := h.FetchJSON(context.Background(), srv.URL, nil)
	ue, ok := base.IsUpstreamError(err)
	if !ok || ue.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected UpstreamError 500, got %v", err)
	}
}
