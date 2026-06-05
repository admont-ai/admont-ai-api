package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDatabaseConfig_DSN(t *testing.T) {
	tests := []struct {
		name string
		cfg  DatabaseConfig
		want string
	}{
		{
			"standard without SSL",
			DatabaseConfig{
				Hostname: "localhost",
				Port:     5432,
				Username: "admin",
				Password: "secret",
				DB:       "mydb",
				SSL:      false,
			},
			"postgres://admin:secret@localhost:5432/mydb?sslmode=disable",
		},
		{
			"with SSL",
			DatabaseConfig{
				Hostname: "db.example.com",
				Port:     5433,
				Username: "user",
				Password: "pass",
				DB:       "production",
				SSL:      true,
			},
			"postgres://user:pass@db.example.com:5433/production?sslmode=require",
		},
		{
			"default dev config",
			DatabaseConfig{
				Hostname: "localhost",
				Port:     5433,
				Username: "admin",
				Password: "admin",
				DB:       "admont-ai",
				SSL:      false,
			},
			"postgres://admin:admin@localhost:5433/admont-ai?sslmode=disable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.cfg.DSN())
		})
	}
}

func TestConfig_Addr(t *testing.T) {
	tests := []struct {
		name     string
		hostname string
		port     int
		want     string
	}{
		{"default", "0.0.0.0", 8080, "0.0.0.0:8080"},
		{"localhost", "localhost", 3000, "localhost:3000"},
		{"custom", "192.168.1.1", 9090, "192.168.1.1:9090"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{Hostname: tt.hostname, Port: tt.port}
			assert.Equal(t, tt.want, cfg.Addr())
		})
	}
}
