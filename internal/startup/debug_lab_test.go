//go:build lab

package startup

import "testing"

func TestLoopback(t *testing.T) {
	for addr, ok := range map[string]bool{
		"127.0.0.1:6060": true, "[::1]:6060": true, "localhost:6060": true,
		"0.0.0.0:6060": false, ":6060": false, "10.0.0.1:6060": false, "127.0.0.1": false,
	} {
		if err := loopback(addr); (err == nil) != ok {
			t.Errorf("%s: %v", addr, err)
		}
	}
}
