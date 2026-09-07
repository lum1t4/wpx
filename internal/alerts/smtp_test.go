package alerts

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/lum1t4/wpx/internal/model"
)

func TestSMTPTLSHandshakeHonorsContextCancellation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			accepted <- conn
		}
	}()
	_, portText, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	err = (SMTPSender{Timeout: time.Second}).Send(ctx, model.SMTPConfig{Host: "127.0.0.1", Port: port, Transport: model.SMTPTLS, From: "alerts@example.com", To: "ops@example.com"}, "test", "test")
	if err == nil || time.Since(started) > 500*time.Millisecond {
		t.Fatalf("cancellation err=%v elapsed=%v", err, time.Since(started))
	}
	select {
	case conn := <-accepted:
		conn.Close()
	default:
	}
}
