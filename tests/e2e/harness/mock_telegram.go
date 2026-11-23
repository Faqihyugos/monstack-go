package harness

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// TelegramMessage captures details of an alert message sent to the mock Telegram Bot API.
type TelegramMessage struct {
	Token               string      `json:"token,omitempty"`
	ChatID              string      `json:"chat_id"`
	Text                string      `json:"text"`
	ParseMode           string      `json:"parse_mode,omitempty"`
	DisableNotification bool        `json:"disable_notification,omitempty"`
	RawBody             []byte      `json:"-"`
	Header              http.Header `json:"-"`
	Timestamp           time.Time   `json:"timestamp"`
}

// TelegramAPIResponse represents standard Telegram Bot API response envelope.
type TelegramAPIResponse struct {
	OK          bool                  `json:"ok"`
	Result      *TelegramResultDetail `json:"result,omitempty"`
	ErrorCode   int                   `json:"error_code,omitempty"`
	Description string                `json:"description,omitempty"`
}

// TelegramResultDetail represents the result payload of a sendMessage call.
type TelegramResultDetail struct {
	MessageID int64         `json:"message_id"`
	Date      int64         `json:"date"`
	Chat      *TelegramChat `json:"chat,omitempty"`
	Text      string        `json:"text,omitempty"`
}

// TelegramChat represents basic chat metadata in Telegram responses.
type TelegramChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

// MockTelegramServer intercepts Telegram Bot API requests (e.g. /bot<token>/sendMessage).
type MockTelegramServer struct {
	server       *httptest.Server
	mu           sync.RWMutex
	messages     []TelegramMessage
	notifyCh     chan struct{}
	failWithCode int
	msgIDCounter atomic.Int64
	closed       bool
}

// NewMockTelegramServer creates and starts an HTTP mock server for Telegram Bot API.
func NewMockTelegramServer() *MockTelegramServer {
	m := &MockTelegramServer{
		notifyCh: make(chan struct{}, 100),
	}
	m.msgIDCounter.Store(100)

	handler := http.HandlerFunc(m.handleRequest)
	m.server = httptest.NewServer(handler)
	return m
}

// handleRequest processes incoming Telegram Bot API calls.
func (m *MockTelegramServer) handleRequest(w http.ResponseWriter, r *http.Request) {
	m.mu.RLock()
	failCode := m.failWithCode
	m.mu.RUnlock()

	// Simulate simulated failure if configured
	if failCode != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(failCode)
		resp := TelegramAPIResponse{
			OK:          false,
			ErrorCode:   failCode,
			Description: fmt.Sprintf("Simulated Telegram error: %d", failCode),
		}
		_ = json.NewEncoder(w).Encode(resp)
		return
	}

	bodyBytes, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()

	// Extract token from path: /bot<token>/sendMessage
	token := ""
	path := r.URL.Path
	if strings.HasPrefix(path, "/bot") {
		parts := strings.SplitN(strings.TrimPrefix(path, "/bot"), "/", 2)
		if len(parts) > 0 {
			token = parts[0]
		}
	}

	msg := TelegramMessage{
		Token:     token,
		RawBody:   bodyBytes,
		Header:    r.Header.Clone(),
		Timestamp: time.Now(),
	}

	// Telegram requests can arrive as application/json or application/x-www-form-urlencoded
	ct := r.Header.Get("Content-Type")
	if strings.Contains(ct, "application/json") || json.Valid(bodyBytes) {
		var rawMap map[string]any
		if err := json.Unmarshal(bodyBytes, &rawMap); err == nil {
			if v, ok := rawMap["text"].(string); ok {
				msg.Text = v
			}
			if v, ok := rawMap["parse_mode"].(string); ok {
				msg.ParseMode = v
			}
			if v, ok := rawMap["disable_notification"].(bool); ok {
				msg.DisableNotification = v
			}
			if v, ok := rawMap["chat_id"]; ok {
				switch cv := v.(type) {
				case string:
					msg.ChatID = cv
				case float64:
					msg.ChatID = strconv.FormatInt(int64(cv), 10)
				case int64:
					msg.ChatID = strconv.FormatInt(cv, 10)
				}
			}
		}
	} else {
		// Form encoded
		values, err := url.ParseQuery(string(bodyBytes))
		if err == nil {
			msg.ChatID = values.Get("chat_id")
			msg.Text = values.Get("text")
			msg.ParseMode = values.Get("parse_mode")
			msg.DisableNotification = values.Get("disable_notification") == "true"
		}
	}

	m.mu.Lock()
	m.messages = append(m.messages, msg)
	m.mu.Unlock()

	select {
	case m.notifyCh <- struct{}{}:
	default:
	}

	// Prepare standard Telegram API 200 response
	msgID := m.msgIDCounter.Add(1)
	chatIDInt, _ := strconv.ParseInt(msg.ChatID, 10, 64)

	resp := TelegramAPIResponse{
		OK: true,
		Result: &TelegramResultDetail{
			MessageID: msgID,
			Date:      time.Now().Unix(),
			Chat: &TelegramChat{
				ID:   chatIDInt,
				Type: "channel",
			},
			Text: msg.Text,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// URL returns the mock server base URL (e.g. "http://127.0.0.1:port").
func (m *MockTelegramServer) URL() string {
	if m.server == nil {
		return ""
	}
	return m.server.URL
}

// Close stops the mock Telegram server.
func (m *MockTelegramServer) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.closed && m.server != nil {
		m.server.Close()
		m.closed = true
	}
}

// GetMessages returns a snapshot copy of all received messages.
func (m *MockTelegramServer) GetMessages() []TelegramMessage {
	m.mu.RLock()
	defer m.mu.RUnlock()
	res := make([]TelegramMessage, len(m.messages))
	copy(res, m.messages)
	return res
}

// MessageCount returns the number of received messages.
func (m *MockTelegramServer) MessageCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.messages)
}

// LastMessage returns the most recently received message or nil if none.
func (m *MockTelegramServer) LastMessage() *TelegramMessage {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.messages) == 0 {
		return nil
	}
	msg := m.messages[len(m.messages)-1]
	return &msg
}

// WaitForMessage blocks until at least one message is received or timeout expires.
func (m *MockTelegramServer) WaitForMessage(timeout time.Duration) (*TelegramMessage, error) {
	deadline := time.After(timeout)

	// First check if a message is already available
	m.mu.RLock()
	if len(m.messages) > 0 {
		msg := m.messages[len(m.messages)-1]
		m.mu.RUnlock()
		return &msg, nil
	}
	m.mu.RUnlock()

	for {
		select {
		case <-m.notifyCh:
			m.mu.RLock()
			if len(m.messages) > 0 {
				msg := m.messages[len(m.messages)-1]
				m.mu.RUnlock()
				return &msg, nil
			}
			m.mu.RUnlock()
		case <-deadline:
			return nil, fmt.Errorf("timeout waiting for Telegram message after %s", timeout)
		}
	}
}

// WaitForMessages blocks until at least count messages have been received or timeout expires.
func (m *MockTelegramServer) WaitForMessages(count int, timeout time.Duration) ([]TelegramMessage, error) {
	deadline := time.After(timeout)

	for {
		m.mu.RLock()
		if len(m.messages) >= count {
			res := make([]TelegramMessage, len(m.messages))
			copy(res, m.messages)
			m.mu.RUnlock()
			return res, nil
		}
		m.mu.RUnlock()

		select {
		case <-m.notifyCh:
		case <-deadline:
			m.mu.RLock()
			got := len(m.messages)
			m.mu.RUnlock()
			return nil, fmt.Errorf("timeout waiting for %d Telegram messages (received %d) after %s", count, got, timeout)
		}
	}
}

// SetFailure causes subsequent requests to fail with the specified HTTP status code.
func (m *MockTelegramServer) SetFailure(statusCode int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failWithCode = statusCode
}

// Reset clears recorded messages and resets failure mode.
func (m *MockTelegramServer) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = nil
	m.failWithCode = 0
	// Drain notify channel
	for {
		select {
		case <-m.notifyCh:
		default:
			return
		}
	}
}
