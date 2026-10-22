package notifier

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestNotifier_Disabled(t *testing.T) {
	var called int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&called, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := New(Config{
		WebhookURL: srv.URL,
		Enabled:    false,
	})

	err := n.SendAlert(context.Background(), "test", "msg", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if atomic.LoadInt32(&called) != 0 {
		t.Fatalf("expected 0 calls when disabled, got %d", called)
	}
}

func TestNotifier_Webhook(t *testing.T) {
	var receivedBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body error: %v", err)
			return
		}
		json.Unmarshal(b, &receivedBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := New(Config{
		WebhookURL: srv.URL,
		Enabled:    true,
	})

	// Test Alert
	err := n.SendAlert(context.Background(), "Node Offline", "Node 1 is down", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedBody["title"] != "🔴 [ALERT] Node Offline" {
		t.Errorf("expected title with alert icon, got %v", receivedBody["title"])
	}
	if receivedBody["is_recovery"] != false {
		t.Errorf("expected is_recovery false, got %v", receivedBody["is_recovery"])
	}

	// Test Recovery
	err = n.SendAlert(context.Background(), "Node Online", "Node 1 is back", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedBody["title"] != "🟢 [RESOLVED] Node Online" {
		t.Errorf("expected title with recovery icon, got %v", receivedBody["title"])
	}
	if receivedBody["is_recovery"] != true {
		t.Errorf("expected is_recovery true, got %v", receivedBody["is_recovery"])
	}
}

func TestNotifier_Telegram(t *testing.T) {
	var receivedBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body error: %v", err)
			return
		}
		json.Unmarshal(b, &receivedBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// In test, custom HTTPClient can redirect Telegram calls to mock server
	client := &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			if strings.Contains(req.URL.Host, "api.telegram.org") {
				req.URL.Scheme = "http"
				req.URL.Host = strings.TrimPrefix(srv.URL, "http://")
			}
			return http.DefaultTransport.RoundTrip(req)
		}),
	}

	n := New(Config{
		TelegramBotToken: "mock-token",
		TelegramChatID:   "12345",
		Enabled:          true,
		HTTPClient:       client,
	})

	err := n.SendAlert(context.Background(), "Drift <Detected> & Broken", "Stack <web> & <db> drifted", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedBody["chat_id"] != "12345" {
		t.Errorf("expected chat_id 12345, got %v", receivedBody["chat_id"])
	}
	text, _ := receivedBody["text"].(string)
	if !strings.Contains(text, "🔴 [ALERT] Drift &lt;Detected&gt; &amp; Broken") {
		t.Errorf("expected text to contain escaped title, got %v", text)
	}
	if !strings.Contains(text, "Stack &lt;web&gt; &amp; &lt;db&gt; drifted") {
		t.Errorf("expected text to contain escaped message, got %v", text)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
