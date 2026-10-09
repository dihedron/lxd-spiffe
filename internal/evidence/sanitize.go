// Package evidence writes the records of lxd-probe and sanitizes everything
// that reaches the evidence tree or the logs (spec: Overview → Evidence
// model).
package evidence

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
)

// Redacted replaces removed values.
const Redacted = "[redacted]"

var (
	// a complete block, or a truncated one (no END line) up to the end of
	// the text, as left by a size limit
	pemBlock = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]+-----(?:[\s\S]*?-----END [A-Z0-9 ]+-----|[\s\S]*$)`)
	jwtToken = regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*`)
	macAddr  = regexp.MustCompile(`(?i)[0-9a-f]{2}([:-])[0-9a-f]{2}(?:[:-][0-9a-f]{2}){4}`)
	// separators of path segments and query values, for name aliasing
	nameSeparators = regexp.MustCompile(`[/?&=]`)
)

// dropHeaders are removed from recorded headers (PRE-03).
var dropHeaders = []string{"Authorization", "Proxy-Authorization", "Cookie", "Set-Cookie"}

// Sanitizer applies the rules of PRE-03 to JSON values, headers and text.
// It is safe for concurrent use. Sanitizing is idempotent: a second pass
// changes nothing, which is what util sanitize --check relies on.
type Sanitizer struct {
	aliases *Aliases
	mu      sync.RWMutex
	secrets []string
	names   map[string]string // real name → kind
}

// NewSanitizer returns a sanitizer that takes its aliases from aliases.
func NewSanitizer(aliases *Aliases) *Sanitizer {
	return &Sanitizer{aliases: aliases, names: map[string]string{}}
}

// AddSecret registers a value that must never appear in the output, such as
// a bearer token or the content of a key file.
func (s *Sanitizer) AddSecret(secret string) {
	if secret == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secrets = append(s.secrets, secret)
}

// RedactName registers an instance or project name to be replaced with its
// alias (--redact-names).
func (s *Sanitizer) RedactName(kind, name string) {
	if name == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.names[name] = kind
}

// String sanitizes text: registered secrets, PEM blocks and JWT-shaped
// tokens are redacted, MAC addresses and registered names replaced with
// their aliases. A name is replaced only as a whole string, a path segment
// or a query value.
func (s *Sanitizer) String(text string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, secret := range s.secrets {
		text = strings.ReplaceAll(text, secret, Redacted)
	}
	text = pemBlock.ReplaceAllLiteralString(text, Redacted)
	text = jwtToken.ReplaceAllLiteralString(text, Redacted)
	text = macAddr.ReplaceAllStringFunc(text, func(mac string) string {
		return s.aliases.Alias(KindMAC, normalizeMAC(mac))
	})
	if len(s.names) > 0 {
		text = s.replaceNames(text)
	}
	return text
}

func (s *Sanitizer) replaceNames(text string) string {
	var b strings.Builder
	last := 0
	for _, sep := range nameSeparators.FindAllStringIndex(text, -1) {
		b.WriteString(s.nameAlias(text[last:sep[0]]))
		b.WriteString(text[sep[0]:sep[1]])
		last = sep[1]
	}
	b.WriteString(s.nameAlias(text[last:]))
	return b.String()
}

func (s *Sanitizer) nameAlias(segment string) string {
	if kind, ok := s.names[segment]; ok {
		return s.aliases.Alias(kind, segment)
	}
	return segment
}

func normalizeMAC(mac string) string {
	return strings.ToLower(strings.ReplaceAll(mac, "-", ":"))
}

// Value returns a sanitized copy of a decoded JSON value (as produced by
// encoding/json into any). The values of cloud-init.*, environment.* and
// user.* keys are redacted, except user.lxd-probe.* and
// user.spire.challenge.*; every string is sanitized with String. Map keys
// are kept as they are.
func (s *Sanitizer) Value(v any) any {
	switch v := v.(type) {
	case string:
		return s.String(v)
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, e := range v {
			if redactedKey(k) {
				out[k] = Redacted
			} else {
				out[k] = s.Value(e)
			}
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, e := range v {
			out[i] = s.Value(e)
		}
		return out
	default:
		return v
	}
}

func redactedKey(key string) bool {
	switch {
	case strings.HasPrefix(key, "user.lxd-probe."), strings.HasPrefix(key, "user.spire.challenge."):
		return false
	case strings.HasPrefix(key, "user."), strings.HasPrefix(key, "cloud-init."), strings.HasPrefix(key, "environment."):
		return true
	}
	return false
}

// JSON sanitizes a JSON document and returns it indented.
func (s *Sanitizer) JSON(data []byte) ([]byte, error) {
	v, err := decodeJSON(data)
	if err != nil {
		return nil, err
	}
	out, err := json.MarshalIndent(s.Value(v), "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding sanitized JSON: %w", err)
	}
	return append(out, '\n'), nil
}

// Headers returns a sanitized copy of h: credential headers are removed and
// the other values sanitized with String.
func (s *Sanitizer) Headers(h http.Header) http.Header {
	out := make(http.Header, len(h))
	for name, values := range h {
		out[name] = make([]string, len(values))
		for i, v := range values {
			out[name][i] = s.String(v)
		}
	}
	for _, name := range dropHeaders {
		out.Del(name)
	}
	return out
}

// decodeJSON decodes one JSON document, keeping numbers exact.
func decodeJSON(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("decoding JSON: %w", err)
	}
	if dec.More() {
		return nil, fmt.Errorf("decoding JSON: trailing data")
	}
	return v, nil
}
