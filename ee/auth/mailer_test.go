// Copyright (c) AIGis authors. All rights reserved.
// This file is part of the AIGis Enterprise Edition and is licensed under the
// AIGis Enterprise Edition License (see ee/LICENSE). It is NOT covered by the
// AGPLv3 that governs the open-source core. Commercial license required for use.

package auth

import (
	"strings"
	"testing"
)

// TestNewMailerNoopWhenHostEmpty: an empty (or whitespace) Host yields a no-op
// mailer with Enabled()==false, so email-gated flows stay off without erroring.
func TestNewMailerNoopWhenHostEmpty(t *testing.T) {
	for _, host := range []string{"", "   "} {
		m, err := NewMailer(SMTPConfig{Host: host})
		if err != nil {
			t.Fatalf("NewMailer(host=%q) err = %v, want nil", host, err)
		}
		if m.Enabled() {
			t.Fatalf("NewMailer(host=%q) Enabled = true, want false (noop)", host)
		}
	}
}

// TestNewMailerMissingCreds: a configured Host with no user/password is an error
// (fail loud rather than silently dropping mail).
func TestNewMailerMissingCreds(t *testing.T) {
	cases := []SMTPConfig{
		{Host: "smtp.example.com", Password: "pw", From: "a@b.c"}, // no user
		{Host: "smtp.example.com", User: "u", From: "a@b.c"},      // no password
	}
	for i, cfg := range cases {
		if _, err := NewMailer(cfg); err == nil {
			t.Fatalf("case %d: NewMailer with missing creds err = nil, want error", i)
		}
	}
}

// TestNewMailerDefaultsAndEnabled: a fully configured host builds a real mailer
// (Enabled true). From defaults to User when omitted; a bare user+password+host
// is accepted (port defaulting is internal).
func TestNewMailerEnabled(t *testing.T) {
	m, err := NewMailer(SMTPConfig{
		Host: "smtp.example.com", User: "u@example.com", Password: "pw",
	})
	if err != nil {
		t.Fatalf("NewMailer err = %v, want nil", err)
	}
	if !m.Enabled() {
		t.Fatal("configured mailer Enabled = false, want true")
	}
	sm, ok := m.(*smtpMailer)
	if !ok {
		t.Fatalf("NewMailer returned %T, want *smtpMailer", m)
	}
	if sm.cfg.Port != 587 {
		t.Fatalf("default port = %d, want 587", sm.cfg.Port)
	}
	if sm.cfg.From != "u@example.com" {
		t.Fatalf("From = %q, want it to default to User", sm.cfg.From)
	}
}

// TestNoopMailerSendNoError: the noop mailer's Send is a silent success.
func TestNoopMailerSendNoError(t *testing.T) {
	if err := (noopMailer{}).Send("a@b.c", "s", "b"); err != nil {
		t.Fatalf("noopMailer.Send err = %v, want nil", err)
	}
}

// TestBuildMessageRFC5322: the assembled message carries the expected headers,
// a blank line separating headers from body, and CRLF line endings.
func TestBuildMessageRFC5322(t *testing.T) {
	msg := string(buildMessage("from@x.io", "to@y.io", "Hello", "the body"))

	for _, want := range []string{
		"From: from@x.io\r\n",
		"To: to@y.io\r\n",
		"Subject: Hello\r\n",
		"MIME-Version: 1.0\r\n",
		"Content-Type: text/plain; charset=UTF-8\r\n",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message missing header %q\n--- full ---\n%s", want, msg)
		}
	}
	// Headers and body must be separated by a blank CRLF line, body last.
	sep := "\r\n\r\n"
	i := strings.Index(msg, sep)
	if i < 0 {
		t.Fatalf("no blank line separating headers from body:\n%s", msg)
	}
	if body := msg[i+len(sep):]; body != "the body" {
		t.Fatalf("body = %q, want %q", body, "the body")
	}
}
