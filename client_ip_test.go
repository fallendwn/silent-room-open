package main

import (
	"net/http/httptest"
	"testing"
)

func TestClientIP(t *testing.T) {
	tests := []struct {
		name       string
		trustProxy bool
		realIP     string
		forwarded  string
		remoteAddr string
		want       string
	}{
		{
			name:       "Railway X-Real-IP takes precedence",
			trustProxy: true,
			realIP:     "203.0.113.10",
			forwarded:  "198.51.100.20, 10.0.0.1",
			remoteAddr: "10.0.0.2:4321",
			want:       "203.0.113.10",
		},
		{
			name:       "trusted proxy falls back to X-Forwarded-For",
			trustProxy: true,
			forwarded:  "198.51.100.20, 10.0.0.1",
			remoteAddr: "10.0.0.2:4321",
			want:       "198.51.100.20",
		},
		{
			name:       "untrusted proxy headers are ignored",
			trustProxy: false,
			realIP:     "203.0.113.10",
			forwarded:  "198.51.100.20",
			remoteAddr: "192.0.2.15:4321",
			want:       "192.0.2.15",
		},
		{
			name:       "malformed proxy headers cannot create a limiter key",
			trustProxy: true,
			realIP:     "not-an-ip",
			forwarded:  "also-not-an-ip, 198.51.100.20",
			remoteAddr: "192.0.2.25:4321",
			want:       "192.0.2.25",
		},
		{
			name:       "IPv6 is normalized",
			trustProxy: true,
			realIP:     "2001:0db8:0:0:0:0:0:1",
			remoteAddr: "[2001:db8::2]:4321",
			want:       "2001:db8::1",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			req.RemoteAddr = test.remoteAddr
			req.Header.Set("X-Real-IP", test.realIP)
			req.Header.Set("X-Forwarded-For", test.forwarded)

			app := &application{trustProxy: test.trustProxy}
			if got := app.clientIP(req); got != test.want {
				t.Fatalf("clientIP() = %q, want %q", got, test.want)
			}
		})
	}
}
