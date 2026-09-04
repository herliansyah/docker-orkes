package notifier

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type Config struct {
	TelegramBotToken string
	TelegramChatID   string
	WebhookURL       string
	Enabled          bool
	HTTPClient       *http.Client
}

type Notifier interface {
	SendAlert(ctx context.Context, title string, message string, isRecovery bool) error
}

type httpNotifier struct {
	cfg Config
}

func New(cfg Config) Notifier {
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &httpNotifier{cfg: cfg}
}

func (n *httpNotifier) SendAlert(ctx context.Context, title string, message string, isRecovery bool) error {
	if !n.cfg.Enabled {
		return nil
	}

	hasTelegram := strings.TrimSpace(n.cfg.TelegramBotToken) != "" && strings.TrimSpace(n.cfg.TelegramChatID) != ""
	hasWebhook := strings.TrimSpace(n.cfg.WebhookURL) != ""

	if !hasTelegram && !hasWebhook {
		return nil
	}

	icon := "🔴"
	statusLabel := "ALERT"
	if isRecovery {
		icon = "🟢"
		statusLabel = "RESOLVED"
	}

	formattedTitle := fmt.Sprintf("%s [%s] %s", icon, statusLabel, title)

	var errs []string

	if hasTelegram {
		if err := n.sendTelegram(ctx, formattedTitle, message); err != nil {
			errs = append(errs, fmt.Sprintf("telegram error: %v", err))
		}
	}

	if hasWebhook {
		if err := n.sendWebhook(ctx, formattedTitle, message, isRecovery); err != nil {
			errs = append(errs, fmt.Sprintf("webhook error: %v", err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

func (n *httpNotifier) sendTelegram(ctx context.Context, title, message string) error {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", n.cfg.TelegramBotToken)
	text := fmt.Sprintf("<b>%s</b>\n\n%s", title, message)

	body, err := json.Marshal(map[string]interface{}{
		"chat_id":    n.cfg.TelegramChatID,
		"text":       text,
		"parse_mode": "HTML",
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.cfg.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("telegram returned HTTP status %d", resp.StatusCode)
	}
	return nil
}

func (n *httpNotifier) sendWebhook(ctx context.Context, title, message string, isRecovery bool) error {
	payload := map[string]interface{}{
		"username":    "Docker Orkes",
		"content":     fmt.Sprintf("**%s**\n%s", title, message),
		"title":       title,
		"message":     message,
		"is_recovery": isRecovery,
		"timestamp":   time.Now().UTC().Format(time.RFC3339),
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.cfg.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.cfg.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("webhook returned HTTP status %d", resp.StatusCode)
	}
	return nil
}
