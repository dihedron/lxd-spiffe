package evidence

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestLogHandler(t *testing.T) {
	var buf bytes.Buffer
	s := NewSanitizer(NewAliases())
	s.AddSecret("s3cr3t-t0ken-value")
	logger := slog.New(NewLogHandler(slog.NewTextHandler(&buf, nil), s))

	logger.With("token", "s3cr3t-t0ken-value").WithGroup("req").Info("calling with s3cr3t-t0ken-value",
		"header", "Bearer "+testJWT,
		"mac", "00:16:3e:aa:bb:cc",
		"err", errors.New("tls: key "+testPEM),
		"status", 200,
		slog.Group("tls", "cert", testPEM),
	)
	out := buf.String()
	for _, leak := range []string{"s3cr3t", "eyJ", "BEGIN", "00:16:3e"} {
		if strings.Contains(out, leak) {
			t.Errorf("log leaks %q: %s", leak, out)
		}
	}
	for _, kept := range []string{"req.status=200", "req.mac=mac-1", "token=" + Redacted} {
		if !strings.Contains(out, kept) {
			t.Errorf("log lacks %q: %s", kept, out)
		}
	}
}
