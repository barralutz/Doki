package api

import (
	"fmt"
	"io"
	"net/http"
	"strings"
)

func headerHasToken(value, want string) bool {
	for _, token := range strings.Split(value, ",") {
		if strings.EqualFold(strings.TrimSpace(token), want) {
			return true
		}
	}
	return false
}

func writeExecHijackResponse(w io.Writer, r *http.Request, contentType string) error {
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("Upgrade")), "tcp") && headerHasToken(r.Header.Get("Connection"), "upgrade") {
		_, err := fmt.Fprintf(w,
			"HTTP/1.1 101 UPGRADED\r\nConnection: Upgrade\r\nUpgrade: tcp\r\nContent-Type: %s\r\n\r\n",
			contentType,
		)
		return err
	}
	_, err := fmt.Fprintf(w, "HTTP/1.1 200 OK\r\nContent-Type: %s\r\n\r\n", contentType)
	return err
}
