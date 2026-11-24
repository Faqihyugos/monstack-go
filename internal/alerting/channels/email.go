package channels

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/smtp"
	"strings"
	"time"

	"github.com/Faqihyugos/monstack-go/pkg/models"
)

// EmailChannel delivers alert notifications via SMTP using Go standard library net/smtp.
type EmailChannel struct {
	name     string
	host     string
	port     string
	username string
	password string
	from     string
	to       []string
	logger   *slog.Logger
}

// NewEmailChannel creates an SMTP Email notification channel from config.
func NewEmailChannel(name string, cfg map[string]any, logger *slog.Logger) (*EmailChannel, error) {
	if logger == nil {
		logger = slog.Default()
	}
	host := getStringConfig(cfg, "smtp_host", "host")
	port := getStringConfig(cfg, "smtp_port", "port")
	if port == "" {
		port = "587"
	}
	from := getStringConfig(cfg, "from")
	if host == "" {
		return nil, errors.New("email channel requires smtp_host/host")
	}
	if from == "" {
		return nil, errors.New("email channel requires from address")
	}

	var to []string
	if rawTo, ok := cfg["to"].([]any); ok {
		for _, item := range rawTo {
			if s := strings.TrimSpace(fmt.Sprintf("%v", item)); s != "" {
				to = append(to, s)
			}
		}
	} else if rawToStr, ok := cfg["to"].([]string); ok {
		to = append(to, rawToStr...)
	} else if s := getStringConfig(cfg, "to"); s != "" {
		for _, part := range strings.Split(s, ",") {
			if p := strings.TrimSpace(part); p != "" {
				to = append(to, p)
			}
		}
	}
	if len(to) == 0 {
		return nil, errors.New("email channel requires at least one 'to' recipient")
	}

	return &EmailChannel{
		name:     name,
		host:     host,
		port:     port,
		username: getStringConfig(cfg, "username"),
		password: getStringConfig(cfg, "password"),
		from:     from,
		to:       to,
		logger:   logger,
	}, nil
}

func (e *EmailChannel) Name() string { return e.name }
func (e *EmailChannel) Type() string { return "email" }

// Send sends a plain-text alert email via SMTP.
func (e *EmailChannel) Send(ctx context.Context, event *models.AlertEvent) error {
	if event == nil {
		return errors.New("alert event cannot be nil")
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}

	subject := fmt.Sprintf("[MonStack][%s] %s - %s",
		strings.ToUpper(string(event.Severity)), event.RuleName, event.TargetName)
	if event.Status == models.AlertStatusResolved {
		subject = fmt.Sprintf("[MonStack][RESOLVED] %s - %s", event.RuleName, event.TargetName)
	}

	msg := fmt.Sprintf(
		"From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n"+
			"Rule: %s\r\nStatus: %s\r\nSeverity: %s\r\nTarget: %s\r\nValue: %.4g\r\nMessage: %s\r\nFired At: %s\r\n",
		e.from,
		strings.Join(e.to, ", "),
		subject,
		time.Now().UTC().Format(time.RFC1123Z),
		event.RuleName,
		strings.ToUpper(string(event.Status)),
		strings.ToUpper(string(event.Severity)),
		event.TargetName,
		event.Value,
		event.Message,
		event.FiredAt.UTC().Format(time.RFC3339),
	)

	var auth smtp.Auth
	if e.username != "" {
		auth = smtp.PlainAuth("", e.username, e.password, e.host)
	}

	addr := fmt.Sprintf("%s:%s", e.host, e.port)
	if err := smtp.SendMail(addr, auth, e.from, e.to, []byte(msg)); err != nil {
		return fmt.Errorf("smtp send failed: %w", err)
	}
	return nil
}
