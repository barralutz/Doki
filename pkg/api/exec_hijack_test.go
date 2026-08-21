package api

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

func TestWriteExecHijackResponseUses101ForTCPUpgrade(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "http://docker/exec/id/start", strings.NewReader(`{"Detach":false,"Tty":false}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "tcp")

	var buf bytes.Buffer
	if err := writeExecHijackResponse(&buf, req, "application/vnd.docker.multiplexed-stream"); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if !strings.HasPrefix(got, "HTTP/1.1 101 UPGRADED\r\n") {
		t.Fatalf("response = %q, want 101 UPGRADED", got)
	}
	for _, want := range []string{
		"Connection: Upgrade\r\n",
		"Upgrade: tcp\r\n",
		"Content-Type: application/vnd.docker.multiplexed-stream\r\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("response missing %q: %q", want, got)
		}
	}
}

func TestWriteExecHijackResponseUses200WithoutUpgrade(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "http://docker/exec/id/start", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := writeExecHijackResponse(&buf, req, "application/vnd.docker.raw-stream"); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if !strings.HasPrefix(got, "HTTP/1.1 200 OK\r\n") {
		t.Fatalf("response = %q, want 200 OK", got)
	}
	if strings.Contains(got, "Upgrade: tcp") {
		t.Fatalf("non-upgrade response unexpectedly contains upgrade headers: %q", got)
	}
}
