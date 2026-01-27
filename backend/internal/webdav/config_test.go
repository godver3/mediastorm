package webdav

import (
	"testing"
)

func TestConfig_Defaults(t *testing.T) {
	// Test that Config struct can be instantiated with expected fields
	cfg := Config{
		Port:   8080,
		User:   "usenet",
		Pass:   "usenet",
		Debug:  false,
		Prefix: "/webdav/",
	}

	if cfg.Port != 8080 {
		t.Errorf("Port = %d, want 8080", cfg.Port)
	}
	if cfg.User != "usenet" {
		t.Errorf("User = %q, want %q", cfg.User, "usenet")
	}
	if cfg.Pass != "usenet" {
		t.Errorf("Pass = %q, want %q", cfg.Pass, "usenet")
	}
	if cfg.Debug != false {
		t.Errorf("Debug = %v, want false", cfg.Debug)
	}
	if cfg.Prefix != "/webdav/" {
		t.Errorf("Prefix = %q, want %q", cfg.Prefix, "/webdav/")
	}
}

func TestConfig_CustomValues(t *testing.T) {
	tests := []struct {
		name   string
		config Config
	}{
		{
			name: "custom port",
			config: Config{
				Port:   9000,
				User:   "admin",
				Pass:   "secret",
				Debug:  true,
				Prefix: "/dav/",
			},
		},
		{
			name: "empty prefix",
			config: Config{
				Port:   8080,
				User:   "user",
				Pass:   "pass",
				Debug:  false,
				Prefix: "",
			},
		},
		{
			name: "root prefix",
			config: Config{
				Port:   8080,
				User:   "user",
				Pass:   "pass",
				Debug:  false,
				Prefix: "/",
			},
		},
		{
			name: "special characters in credentials",
			config: Config{
				Port:   8080,
				User:   "user@domain.com",
				Pass:   "p@ss!word#$%",
				Debug:  false,
				Prefix: "/webdav/",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Verify all fields are accessible
			_ = tt.config.Port
			_ = tt.config.User
			_ = tt.config.Pass
			_ = tt.config.Debug
			_ = tt.config.Prefix
		})
	}
}

func TestConfig_ZeroValue(t *testing.T) {
	var cfg Config

	if cfg.Port != 0 {
		t.Errorf("zero value Port = %d, want 0", cfg.Port)
	}
	if cfg.User != "" {
		t.Errorf("zero value User = %q, want empty", cfg.User)
	}
	if cfg.Pass != "" {
		t.Errorf("zero value Pass = %q, want empty", cfg.Pass)
	}
	if cfg.Debug != false {
		t.Errorf("zero value Debug = %v, want false", cfg.Debug)
	}
	if cfg.Prefix != "" {
		t.Errorf("zero value Prefix = %q, want empty", cfg.Prefix)
	}
}
