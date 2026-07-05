// Copyright (c) AIGis authors. All rights reserved.
// This file is part of the AIGis Enterprise Edition and is licensed under the
// AIGis Enterprise Edition License (see ee/LICENSE). It is NOT covered by the
// AGPLv3 that governs the open-source core. Commercial license required for use.

package auth

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// Mailer sends a transactional email (verification / password-reset). It is an
// interface so handlers can be unit-tested with an in-memory fake and so the
// deployment can run without SMTP configured (noopMailer), in which case the
// email-dependent flows (email verify, password reset) simply stay disabled.
type Mailer interface {
	// Send delivers a plain-text message to a single recipient. Enabled reports
	// whether a real transport is wired: false means SMTP is not configured, so
	// callers should treat email-gated features as off.
	Send(to, subject, body string) error
	Enabled() bool
}

// SMTPConfig is the transport configuration, sourced from environment variables
// (never committed): host/port of the SMTP server, the login user/password, and
// the From address. Port 465 uses implicit TLS (SMTPS); 587 (or others) use
// STARTTLS. An empty Host yields a noopMailer.
type SMTPConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	From     string
}

// noopMailer is installed when SMTP is not configured. It reports Enabled()
// false so email-gated flows (verify/reset) are disabled rather than silently
// dropping mail.
type noopMailer struct{}

func (noopMailer) Send(_, _, _ string) error { return nil }
func (noopMailer) Enabled() bool             { return false }

// smtpMailer is the real transport. It dials the SMTP server with either
// implicit TLS (port 465) or STARTTLS (587/other), authenticates with PLAIN,
// and sends a single-recipient plain-text message.
type smtpMailer struct {
	cfg SMTPConfig
}

// NewMailer returns a Mailer for the given config. An empty Host (SMTP not
// configured) yields a noopMailer so the gateway still runs, with email-gated
// features off. A configured host requires user/password/from to be set.
func NewMailer(cfg SMTPConfig) (Mailer, error) {
	if strings.TrimSpace(cfg.Host) == "" {
		return noopMailer{}, nil
	}
	if cfg.Port == 0 {
		cfg.Port = 587
	}
	if cfg.From == "" {
		cfg.From = cfg.User
	}
	if cfg.User == "" || cfg.Password == "" || cfg.From == "" {
		return nil, fmt.Errorf("mailer: host set but user/password/from missing")
	}
	return &smtpMailer{cfg: cfg}, nil
}

func (m *smtpMailer) Enabled() bool { return true }

// Send delivers one plain-text email. Port 465 -> implicit TLS; anything else
// (typically 587) -> plaintext dial then STARTTLS upgrade.
func (m *smtpMailer) Send(to, subject, body string) error {
	addr := net.JoinHostPort(m.cfg.Host, fmt.Sprintf("%d", m.cfg.Port))
	msg := buildMessage(m.cfg.From, to, subject, body)
	auth := smtp.PlainAuth("", m.cfg.User, m.cfg.Password, m.cfg.Host)

	if m.cfg.Port == 465 {
		return m.sendImplicitTLS(addr, auth, to, msg)
	}
	return m.sendSTARTTLS(addr, auth, to, msg)
}

// sendImplicitTLS dials a TLS connection first (SMTPS, port 465), then speaks
// SMTP over it.
func (m *smtpMailer) sendImplicitTLS(addr string, auth smtp.Auth, to string, msg []byte) error {
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 15 * time.Second}, "tcp", addr,
		&tls.Config{ServerName: m.cfg.Host, MinVersion: tls.VersionTLS12})
	if err != nil {
		return fmt.Errorf("mailer: tls dial: %w", err)
	}
	c, err := smtp.NewClient(conn, m.cfg.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("mailer: smtp client: %w", err)
	}
	defer c.Close()
	return deliver(c, auth, m.cfg.From, to, msg)
}

// sendSTARTTLS dials plaintext (port 587), then upgrades to TLS via STARTTLS
// before authenticating.
func (m *smtpMailer) sendSTARTTLS(addr string, auth smtp.Auth, to string, msg []byte) error {
	d := net.Dialer{Timeout: 15 * time.Second}
	conn, err := d.Dial("tcp", addr)
	if err != nil {
		return fmt.Errorf("mailer: dial: %w", err)
	}
	c, err := smtp.NewClient(conn, m.cfg.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("mailer: smtp client: %w", err)
	}
	defer c.Close()
	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: m.cfg.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("mailer: starttls: %w", err)
		}
	}
	return deliver(c, auth, m.cfg.From, to, msg)
}

// deliver runs the AUTH + MAIL/RCPT/DATA sequence on an established client.
func deliver(c *smtp.Client, auth smtp.Auth, from, to string, msg []byte) error {
	if ok, _ := c.Extension("AUTH"); ok {
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("mailer: auth: %w", err)
		}
	}
	if err := c.Mail(from); err != nil {
		return fmt.Errorf("mailer: mail from: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("mailer: rcpt to: %w", err)
	}
	wc, err := c.Data()
	if err != nil {
		return fmt.Errorf("mailer: data: %w", err)
	}
	if _, err := wc.Write(msg); err != nil {
		_ = wc.Close()
		return fmt.Errorf("mailer: write body: %w", err)
	}
	if err := wc.Close(); err != nil {
		return fmt.Errorf("mailer: close body: %w", err)
	}
	return c.Quit()
}

// buildMessage assembles a minimal RFC 5322 message with UTF-8 headers/body.
func buildMessage(from, to, subject, body string) []byte {
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + to + "\r\n")
	b.WriteString("Subject: " + subject + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(body)
	return []byte(b.String())
}
