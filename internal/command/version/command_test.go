package version

import "testing"

func TestRelevantEnv(t *testing.T) {
	tests := []struct {
		env, want string
		ok        bool
	}{
		{"LXD_SPIFFE_DOTENV=/etc/lxd-spiffe/.env", "LXD_SPIFFE_DOTENV=/etc/lxd-spiffe/.env", true},
		{"LXD_AGENT_PLUGIN_LOG_LEVEL=debug", "LXD_AGENT_PLUGIN_LOG_LEVEL=debug", true},
		{"LXD_TRUST_PASSWORD=hunter2", "LXD_TRUST_PASSWORD=********", true},
		{"LXD_CLIENT_SECRET=s3cr3t", "LXD_CLIENT_SECRET=********", true},
		{"OS_AUTH_URL=https://keystone:5000/v3", "", false},
		{"HOME=/root", "", false},
		{"MIDPOINT_PASSWORD=x", "", false},
	}
	for _, tt := range tests {
		got, ok := relevantEnv(tt.env)
		if got != tt.want || ok != tt.ok {
			t.Errorf("relevantEnv(%q) = %q, %v; want %q, %v", tt.env, got, ok, tt.want, tt.ok)
		}
	}
}
