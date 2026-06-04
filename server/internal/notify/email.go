package notify

import (
	"context"
	"fmt"
	"net"
	"net/smtp"
	"strings"
)

// EmailConfig holds SMTP settings.
type EmailConfig struct {
	Host        string
	Port        int
	User        string
	Password    string
	TLSMode     string // "none", "starttls", "tls"
	FromAddress string
	FromName    string
}

// EmailSender sends notification emails via SMTP.
type EmailSender struct {
	ConfigFn func() EmailConfig
}

// Send delivers an email for the given event to the recipient.
func (s *EmailSender) Send(_ context.Context, to string, ev Event) error {
	cfg := s.ConfigFn()
	if cfg.Host == "" || cfg.FromAddress == "" {
		return nil
	}

	from := cfg.FromAddress
	if cfg.FromName != "" {
		from = cfg.FromName + " <" + cfg.FromAddress + ">"
	}

	subject := severityPrefix(ev.Severity) + ev.Title

	body := buildEmailBody(ev, from)

	addr := net.JoinHostPort(cfg.Host, fmt.Sprintf("%d", cfg.Port))

	var auth_ smtp.Auth
	if cfg.User != "" {
		auth_ = smtp.PlainAuth("", cfg.User, cfg.Password, cfg.Host)
	}

	msg := "From: " + from + "\r\n" +
		"To: " + to + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n" +
		"\r\n" +
		body

	return smtp.SendMail(addr, auth_, cfg.FromAddress, []string{to}, []byte(msg))
}

// SendTest sends a test email to verify SMTP configuration.
func (s *EmailSender) SendTest(to string) error {
	ev := Event{
		Name:     "system.test",
		Severity: SevInfo,
		Title:    "Test notification from AutoDeploy",
		Body:     "If you received this email, your SMTP configuration is working correctly.",
	}
	return s.Send(context.Background(), to, ev)
}

func severityPrefix(sev string) string {
	switch sev {
	case SevError:
		return "[ERROR] "
	case SevWarning:
		return "[WARNING] "
	case SevSuccess:
		return "[OK] "
	default:
		return ""
	}
}

func buildEmailBody(ev Event, _ string) string {
	var sb strings.Builder
	sb.WriteString(`<!DOCTYPE html><html><body style="font-family:sans-serif;max-width:600px;margin:0 auto;padding:20px;">`)
	sb.WriteString(`<div style="border-left:4px solid `)
	sb.WriteString(severityColor(ev.Severity))
	sb.WriteString(`;padding:12px 16px;margin:16px 0;background:#f8f9fa;">`)
	sb.WriteString(`<h2 style="margin:0 0 8px 0;font-size:18px;">`)
	sb.WriteString(ev.Title)
	sb.WriteString(`</h2>`)
	if ev.Body != "" {
		sb.WriteString(`<p style="margin:0 0 8px 0;color:#555;">`)
		sb.WriteString(ev.Body)
		sb.WriteString(`</p>`)
	}
	sb.WriteString(`<p style="margin:0;font-size:12px;color:#888;">Event: `)
	sb.WriteString(ev.Name)
	sb.WriteString(` | Severity: `)
	sb.WriteString(ev.Severity)
	if !ev.OccurredAt.IsZero() {
		sb.WriteString(` | `)
		sb.WriteString(ev.OccurredAt.Format("2006-01-02 15:04:05 UTC"))
	}
	sb.WriteString(`</p>`)
	sb.WriteString(`</div>`)
	if ev.Link != "" {
		sb.WriteString(`<p><a href="`)
		sb.WriteString(ev.Link)
		sb.WriteString(`" style="display:inline-block;padding:8px 16px;background:#0066cc;color:#fff;text-decoration:none;border-radius:4px;">View in Portal</a></p>`)
	}
	sb.WriteString(`<p style="font-size:11px;color:#999;margin-top:24px;">Sent by AutoDeploy notification system.</p>`)
	sb.WriteString(`</body></html>`)
	return sb.String()
}

func severityColor(sev string) string {
	switch sev {
	case SevError:
		return "#dc3545"
	case SevWarning:
		return "#ffc107"
	case SevSuccess:
		return "#28a745"
	default:
		return "#0066cc"
	}
}
