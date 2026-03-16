package config

import (
	"encoding/json"
	"fmt"
	"os"
)

// ParamConfig описывает конфигурацию одного параметра (диапазон, дефолт)
type ParamConfig struct {
	Min     int `json:"min"`
	Max     int `json:"max"`
	Default int `json:"default"`
}

type MusicFile struct {
	Name        string
	Description string
}

// TargetConfig конфигурация одной целевой точки (ключ = IP)
type TargetConfig struct {
	Title       string                 `json:"title"`
	Protocol    string                 `json:"protocol"`
	PlayQuery   string                 `json:"playQuery"`
	StopQuery   string                 `json:"stopQuery"`
	ConfigQuery string                 `json:"configQuery"`
	Params      map[string]ParamConfig `json:"params"`
	MusicFiles  []MusicFile            `json:"-"`
	Alias       string                 `json:"-"`
}

// BruteforceConfig защита от перебора пароля
type BruteforceConfig struct {
	MaxAttempts    int `json:"maxAttempts"`
	LockoutMinutes int `json:"lockoutMinutes"`
}

// AppConfig конфигурация самого приложения
type AppConfig struct {
	ListenHost            string           `json:"listenHost"`
	ListenPort            string           `json:"listenPort"`
	SecretHash            string           `json:"secretHash"`
	Bruteforce            BruteforceConfig `json:"bruteforce"`
	SessionTimeoutMinutes int              `json:"sessionTimeoutMinutes"`
	RepeatDurationSeconds int              `json:"repeatDurationSeconds"` // новое поле
}

// Config основная структура конфигурации
type Config struct {
	App       AppConfig                `json:"app"`
	Targets   map[string]*TargetConfig `json:"targets"` // ключ = IP
	AliasToIP map[string]string        `json:"-"`       // runtime‑карта: alias -> IP
}

// Load читает config.json
func Load(filename string) (*Config, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("не удалось открыть %s: %w", filename, err)
	}
	defer f.Close()

	var raw struct {
		App     AppConfig               `json:"app"`
		Targets map[string]TargetConfig `json:"targets"`
	}
	if err := json.NewDecoder(f).Decode(&raw); err != nil {
		return nil, fmt.Errorf("ошибка парсинга JSON: %w", err)
	}

	cfg := &Config{
		App:       raw.App,
		Targets:   make(map[string]*TargetConfig),
		AliasToIP: make(map[string]string),
	}

	i := 1
	for ip, t := range raw.Targets {
		tCopy := t
		tCopy.Alias = fmt.Sprintf("host%d", i) // или по IP
		cfg.Targets[ip] = &tCopy
		cfg.AliasToIP[tCopy.Alias] = ip
		i++
	}

	// Дефолты
	if cfg.App.ListenHost == "" {
		cfg.App.ListenHost = "localhost"
	}
	if cfg.App.ListenPort == "" {
		cfg.App.ListenPort = ":8080"
	}
	if cfg.App.Bruteforce.MaxAttempts == 0 {
		cfg.App.Bruteforce.MaxAttempts = 5
	}
	if cfg.App.Bruteforce.LockoutMinutes == 0 {
		cfg.App.Bruteforce.LockoutMinutes = 15
	}
	if cfg.App.SessionTimeoutMinutes == 0 {
		cfg.App.SessionTimeoutMinutes = 5
	}
	if cfg.App.RepeatDurationSeconds == 0 {
		cfg.App.RepeatDurationSeconds = 10 // или любой дефолт
	}
	return cfg, nil
}
