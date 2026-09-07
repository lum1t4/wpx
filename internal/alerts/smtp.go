package alerts

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/smtp"
	"strings"
	"time"

	"github.com/lum1t4/wpx/internal/model"
)

type Sender interface {
	Send(context.Context, model.SMTPConfig, string, string) error
}

type SMTPSender struct{ Timeout time.Duration }

func (s SMTPSender) Send(ctx context.Context, config model.SMTPConfig, subject, body string) error {
	if err := model.ValidateSMTPConfig(config); err != nil {
		return err
	}
	if strings.ContainsAny(subject, "\r\n") {
		return fmt.Errorf("mail subject contains a line break")
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	address := net.JoinHostPort(config.Host, fmt.Sprint(config.Port))
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("connect to SMTP server: %w", err)
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: config.Host}
	if config.Transport == model.SMTPTLS {
		secured := tls.Client(conn, tlsConfig)
		if err := secured.HandshakeContext(ctx); err != nil {
			conn.Close()
			return fmt.Errorf("secure SMTP session: %w", err)
		}
		conn = secured
	}
	defer conn.Close()
	deadline := time.Now().Add(timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	_ = conn.SetDeadline(deadline)
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.SetDeadline(time.Now())
		case <-done:
		}
	}()
	client, err := smtp.NewClient(conn, config.Host)
	if err != nil {
		return fmt.Errorf("start SMTP session: %w", err)
	}
	defer client.Close()
	if config.Transport == model.SMTPSTARTTLS {
		ok, _ := client.Extension("STARTTLS")
		if !ok {
			return fmt.Errorf("SMTP server does not offer STARTTLS")
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			return fmt.Errorf("secure SMTP session: %w", err)
		}
	}
	if config.Username != "" {
		if config.Password == "" {
			return fmt.Errorf("SMTP password is required with a username")
		}
		if err := client.Auth(smtp.PlainAuth("", config.Username, config.Password, config.Host)); err != nil {
			return fmt.Errorf("authenticate to SMTP server: %w", err)
		}
	}
	if err := client.Mail(config.From); err != nil {
		return fmt.Errorf("set SMTP sender: %w", err)
	}
	if err := client.Rcpt(config.To); err != nil {
		return fmt.Errorf("set SMTP recipient: %w", err)
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("start SMTP message: %w", err)
	}
	message := "From: " + config.From + "\r\nTo: " + config.To + "\r\nSubject: " + subject + "\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n" + normalizeMailBody(body)
	if _, err := io.Copy(writer, bufio.NewReader(strings.NewReader(message))); err != nil {
		writer.Close()
		return fmt.Errorf("write SMTP message: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("finish SMTP message: %w", err)
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("finish SMTP session: %w", err)
	}
	return nil
}

func normalizeMailBody(body string) string {
	body = strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\r", "\n")
	return strings.ReplaceAll(body, "\n", "\r\n")
}
