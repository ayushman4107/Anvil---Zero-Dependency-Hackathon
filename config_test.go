package main

import "testing"

func TestDefaultConfigValid(t *testing.T) {
	if err := DefaultConfig().Validate(); err != nil {
		t.Fatalf("DefaultConfig().Validate() = %v", err)
	}
}

func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "missing port", mutate: func(c *Config) { c.Listen = "localhost" }},
		{name: "named port", mutate: func(c *Config) { c.Listen = "localhost:http" }},
		{name: "zero connection limit", mutate: func(c *Config) { c.MaxConnections = 0 }},
		{name: "zero read timeout", mutate: func(c *Config) { c.ReadTimeoutMS = 0 }},
		{name: "zero write timeout", mutate: func(c *Config) { c.WriteTimeoutMS = 0 }},
		{name: "zero timeout", mutate: func(c *Config) { c.IdleTimeoutMS = 0 }},
		{name: "zero shutdown timeout", mutate: func(c *Config) { c.ShutdownMS = 0 }},
		{name: "zero force-close timeout", mutate: func(c *Config) { c.ForceCloseMS = 0 }},
		{name: "zero request limit", mutate: func(c *Config) { c.MaxRequests = 0 }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := DefaultConfig()
			test.mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("Validate() = nil, want an error")
			}
		})
	}
}
