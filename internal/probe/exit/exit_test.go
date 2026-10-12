package exit

import (
	"errors"
	"fmt"
	"testing"
)

func TestCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, 0},
		{"plain error", errors.New("boom"), Internal},
		{"coded", New(Refused, errors.New("missing --lab")), Refused},
		{"wrapped coded", fmt.Errorf("config set: %w", New(Connection, errors.New("pin mismatch"))), Connection},
		{"coded without cause", New(Strict, nil), Strict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Code(tt.err); got != tt.want {
				t.Errorf("Code(%v) = %d; want %d", tt.err, got, tt.want)
			}
		})
	}
}

func TestErrorUnwrap(t *testing.T) {
	cause := errors.New("pin mismatch")
	err := New(Connection, cause)
	if !errors.Is(err, cause) {
		t.Errorf("errors.Is(%v, cause) = false; want true", err)
	}
	if err.Error() != "pin mismatch" {
		t.Errorf("Error() = %q; want %q", err.Error(), "pin mismatch")
	}
}
