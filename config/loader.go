// Copyright 2025 Admilson B. F. Cossa
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
// Package config provides configuration management
// Author: Admilson B. F. Cossa

package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-gen-ecosystem/halolog/types"
	"go.yaml.in/yaml/v4"
)

// AppenderConfig represents an appender configuration
type AppenderConfig struct {
	Type     string          `yaml:"type" json:"type"`
	Path     string          `yaml:"path,omitempty" json:"path,omitempty"`
	Rotation *RotationConfig `yaml:"rotation,omitempty" json:"rotation,omitempty"`
}

// RotationConfig represents log rotation configuration
type RotationConfig struct {
	MaxSize    int  `yaml:"max_size" json:"max_size"`
	MaxBackups int  `yaml:"max_backups" json:"max_backups"`
	MaxAge     int  `yaml:"max_age" json:"max_age"`
	Compress   bool `yaml:"compress" json:"compress"`
}

// SecurityConfig represents security configuration
type SecurityConfig struct {
	PIIPatterns       []PIIPattern `yaml:"pii_patterns" json:"pii_patterns"`
	MaskSensitiveData bool         `yaml:"mask_sensitive_data" json:"mask_sensitive_data"`
}

// PIIPattern represents a PII masking pattern
type PIIPattern struct {
	Name        string `yaml:"name" json:"name"`
	Pattern     string `yaml:"pattern" json:"pattern"`
	Replacement string `yaml:"replacement" json:"replacement"`
}

// SensitiveFieldConfig represents a sensitive field configuration for O(1) lookup
type SensitiveFieldConfig struct {
	Name    string `yaml:"name" json:"name"`
	FieldID int    `yaml:"field_id" json:"field_id"` // Pre-computed FieldDict ID for O(1) lookup
	Mask    string `yaml:"mask" json:"mask"`
}

// StartupConfig represents startup registration configuration for O(1) field access
type StartupConfig struct {
	CustomFields      []string               `yaml:"custom_fields" json:"custom_fields"`             // Register ALL fields at startup
	SensitiveFields   []SensitiveFieldConfig `yaml:"sensitive_fields" json:"sensitive_fields"`       // O(1) lookup configuration
	FieldColors       map[string]types.Color `yaml:"field_colors" json:"field_colors"`               // O(1) color mapping
	OptimalBufferSize int                    `yaml:"optimal_buffer_size" json:"optimal_buffer_size"` // Calculated from profiling
	ContextFields     *ContextFieldsConfig   `yaml:"context_fields" json:"context_fields"`           // High-performance context field auto-loading
}

// ContextFieldsConfig represents High-performance context field auto-loading configuration
type ContextFieldsConfig struct {
	Enabled bool     `yaml:"enabled" json:"enabled"` // Enable automatic context field loading
	Fields  []string `yaml:"fields" json:"fields"`   // Fields to automatically extract from context
}

// PerformanceConfig represents performance configuration
type PerformanceConfig struct {
	RateLimit     int           `yaml:"rate_limit" json:"rate_limit"`
	BufferSize    int           `yaml:"buffer_size" json:"buffer_size"`
	FlushInterval time.Duration `yaml:"flush_interval" json:"flush_interval"`
}

// EnvironmentConfig represents environment configuration
type EnvironmentConfig struct {
	DefaultLevel     types.LogLevel `yaml:"default_level" json:"default_level"`
	ProductionLevel  types.LogLevel `yaml:"production_level" json:"production_level"`
	DevelopmentLevel types.LogLevel `yaml:"development_level" json:"development_level"`
}

// OutputConfig represents an output configuration for fluent API
type OutputConfig struct {
	Type   string      `yaml:"type" json:"type"`     // Output type: json, simple, etc.
	Target string      `yaml:"target" json:"target"` // Target: buffer, console, file, etc.
	Buffer interface{} `yaml:"-" json:"-"`           // Buffer reference for buffer target
}

// HaloLogConfig represents the complete HaloLogger configuration
type HaloLogConfig struct {
	Component           string            `yaml:"component" json:"component"`
	Environment         string            `yaml:"environment" json:"environment"`
	Outputs             []OutputConfig    `yaml:"outputs" json:"outputs"` // Fluent API outputs
	Loggers             []LoggerConfig    `yaml:"loggers" json:"loggers"` // Traditional loggers
	Security            SecurityConfig    `yaml:"security" json:"security"`
	Performance         PerformanceConfig `yaml:"performance" json:"performance"`
	EnvConfig           EnvironmentConfig `yaml:"env_config" json:"env_config"`                     // Environment-specific config
	PerformanceProfile  string            `yaml:"performance_profile" json:"performance_profile"`   // Performance profile: "zero-allocation", "high-frequency", "standard", "adaptive"
	StartupRegistration StartupConfig     `yaml:"startup_registration" json:"startup_registration"` // Startup field registration for O(1) access
}

// LoggerConfig represents a logger configuration
type LoggerConfig struct {
	Name      string           `yaml:"name" json:"name"`
	Level     types.LogLevel   `yaml:"level" json:"level"`
	Appenders []AppenderConfig `yaml:"appenders" json:"appenders"`
}

// ConfigLoader handles loading configuration from various sources.
// It is established public API returned by NewConfigLoader and referenced across
// the module and docs; renaming would break the public API.
//
//nolint:revive // intentional stutter retained to preserve the public API name.
type ConfigLoader struct {
	envPrefix string
}

// NewConfigLoader creates a new configuration loader
func NewConfigLoader(envPrefix string) *ConfigLoader {
	return &ConfigLoader{
		envPrefix: envPrefix,
	}
}

// ParseLogLevel converts a string to LogLevel
func ParseLogLevel(level string) types.LogLevel {
	switch strings.ToUpper(level) {
	case "TRACE":
		return types.TraceLevel
	case "DEBUG":
		return types.DebugLevel
	case "INFO":
		return types.InfoLevel
	case "WARN", "WARNING":
		return types.WarnLevel
	case "ERROR":
		return types.ErrorLevel
	case "FATAL":
		return types.FatalLevel
	case "PANIC":
		return types.PanicLevel
	default:
		return types.InfoLevel // Default to INFO for invalid levels
	}
}

// validateLogLevel checks if a log level string is valid
func validateLogLevel(level string) error {
	switch strings.ToUpper(level) {
	case "TRACE", "DEBUG", "INFO", "WARN", "WARNING", "ERROR", "FATAL", "PANIC":
		return nil
	default:
		return fmt.Errorf("invalid log level: %s", level)
	}
}

// LoadFromFile loads configuration from a file (YAML or JSON). The path is
// supplied explicitly by the caller — reading exactly the file they name is the
// documented contract of this API — so the G304 file-inclusion flag is expected.
func (c *ConfigLoader) LoadFromFile(path string) (*HaloLogConfig, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: caller-supplied config path is the intended API contract
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", path, err)
	}

	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".yaml", ".yml":
		return c.LoadFromYAML(data)
	case ".json":
		return c.LoadFromJSON(data)
	default:
		return nil, fmt.Errorf("unsupported config file format: %s", ext)
	}
}

// LoadFromYAML loads configuration from YAML data
func (c *ConfigLoader) LoadFromYAML(data []byte) (*HaloLogConfig, error) {
	// First, unmarshal to a temporary struct with string levels
	var tempConfig struct {
		Loggers []struct {
			Name      string           `yaml:"name" json:"name"`
			Level     string           `yaml:"level" json:"level"`
			Appenders []AppenderConfig `yaml:"appenders" json:"appenders"`
		} `yaml:"loggers" json:"loggers"`
		Security    SecurityConfig    `yaml:"security" json:"security"`
		Performance PerformanceConfig `yaml:"performance" json:"performance"`
		Environment struct {
			DefaultLevel     string `yaml:"default_level" json:"default_level"`
			ProductionLevel  string `yaml:"production_level" json:"production_level"`
			DevelopmentLevel string `yaml:"development_level" json:"development_level"`
		} `yaml:"environment" json:"environment"`
		StartupRegistration StartupConfig `yaml:"startup_registration" json:"startup_registration"`
	}

	if err := yaml.Unmarshal(data, &tempConfig); err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}

	// Validate log levels
	for _, logger := range tempConfig.Loggers {
		if err := validateLogLevel(logger.Level); err != nil {
			return nil, fmt.Errorf("invalid logger level for '%s': %w", logger.Name, err)
		}
	}
	if err := validateLogLevel(tempConfig.Environment.DefaultLevel); err != nil {
		return nil, fmt.Errorf("invalid default_level: %w", err)
	}
	if err := validateLogLevel(tempConfig.Environment.ProductionLevel); err != nil {
		return nil, fmt.Errorf("invalid production_level: %w", err)
	}
	if err := validateLogLevel(tempConfig.Environment.DevelopmentLevel); err != nil {
		return nil, fmt.Errorf("invalid development_level: %w", err)
	}

	// Convert to the final config with proper LogLevel types
	config := &HaloLogConfig{
		Security:            tempConfig.Security,
		Performance:         tempConfig.Performance,
		StartupRegistration: tempConfig.StartupRegistration,
	}

	// Convert loggers
	for _, logger := range tempConfig.Loggers {
		config.Loggers = append(config.Loggers, LoggerConfig{
			Name:      logger.Name,
			Level:     ParseLogLevel(logger.Level),
			Appenders: logger.Appenders,
		})
	}

	// Convert environment levels
	config.EnvConfig = EnvironmentConfig{
		DefaultLevel:     ParseLogLevel(tempConfig.Environment.DefaultLevel),
		ProductionLevel:  ParseLogLevel(tempConfig.Environment.ProductionLevel),
		DevelopmentLevel: ParseLogLevel(tempConfig.Environment.DevelopmentLevel),
	}

	return config, nil
}

// LoadFromJSON loads configuration from JSON data
func (c *ConfigLoader) LoadFromJSON(data []byte) (*HaloLogConfig, error) {
	// First, unmarshal to a temporary struct with string levels and duration
	var tempConfig struct {
		Loggers []struct {
			Name      string           `yaml:"name" json:"name"`
			Level     string           `yaml:"level" json:"level"`
			Appenders []AppenderConfig `yaml:"appenders" json:"appenders"`
		} `yaml:"loggers" json:"loggers"`
		Security    SecurityConfig `yaml:"security" json:"security"`
		Performance struct {
			RateLimit     int    `yaml:"rate_limit" json:"rate_limit"`
			BufferSize    int    `yaml:"buffer_size" json:"buffer_size"`
			FlushInterval string `yaml:"flush_interval" json:"flush_interval"`
		} `yaml:"performance" json:"performance"`
		Environment struct {
			DefaultLevel     string `yaml:"default_level" json:"default_level"`
			ProductionLevel  string `yaml:"production_level" json:"production_level"`
			DevelopmentLevel string `yaml:"development_level" json:"development_level"`
		} `yaml:"environment" json:"environment"`
		StartupRegistration StartupConfig `yaml:"startup_registration" json:"startup_registration"`
	}

	if err := json.Unmarshal(data, &tempConfig); err != nil {
		return nil, fmt.Errorf("failed to parse JSON: %w", err)
	}

	// Validate log levels
	for _, logger := range tempConfig.Loggers {
		if err := validateLogLevel(logger.Level); err != nil {
			return nil, fmt.Errorf("invalid logger level for '%s': %w", logger.Name, err)
		}
	}
	if err := validateLogLevel(tempConfig.Environment.DefaultLevel); err != nil {
		return nil, fmt.Errorf("invalid default_level: %w", err)
	}
	if err := validateLogLevel(tempConfig.Environment.ProductionLevel); err != nil {
		return nil, fmt.Errorf("invalid production_level: %w", err)
	}
	if err := validateLogLevel(tempConfig.Environment.DevelopmentLevel); err != nil {
		return nil, fmt.Errorf("invalid development_level: %w", err)
	}

	// Parse the flush interval duration
	flushInterval, err := time.ParseDuration(tempConfig.Performance.FlushInterval)
	if err != nil {
		return nil, fmt.Errorf("invalid flush_interval format: %w", err)
	}

	// Convert to the final config with proper types
	config := &HaloLogConfig{
		Security: tempConfig.Security,
		Performance: PerformanceConfig{
			RateLimit:     tempConfig.Performance.RateLimit,
			BufferSize:    tempConfig.Performance.BufferSize,
			FlushInterval: flushInterval,
		},
		StartupRegistration: tempConfig.StartupRegistration,
	}

	// Convert loggers
	for _, logger := range tempConfig.Loggers {
		config.Loggers = append(config.Loggers, LoggerConfig{
			Name:      logger.Name,
			Level:     ParseLogLevel(logger.Level),
			Appenders: logger.Appenders,
		})
	}

	// Convert environment levels
	config.EnvConfig = EnvironmentConfig{
		DefaultLevel:     ParseLogLevel(tempConfig.Environment.DefaultLevel),
		ProductionLevel:  ParseLogLevel(tempConfig.Environment.ProductionLevel),
		DevelopmentLevel: ParseLogLevel(tempConfig.Environment.DevelopmentLevel),
	}

	return config, nil
}

// LoadFromEnvironment loads configuration from environment variables
func (c *ConfigLoader) LoadFromEnvironment() (*HaloLogConfig, error) {
	// Create a simple config from environment
	config := &HaloLogConfig{
		Loggers: []LoggerConfig{
			{
				Name:  c.getEnvString("LOGGER_NAME", "default"),
				Level: ParseLogLevel(c.getEnvString("LOGGER_LEVEL", "INFO")),
				Appenders: []AppenderConfig{
					{
						Type: c.getEnvString("APPENDER_TYPE", "console"),
						Path: c.getEnvString("FILE_PATH", ""),
					},
				},
			},
		},
		Security: SecurityConfig{
			MaskSensitiveData: c.getEnvBool("MASK_SENSITIVE_DATA", true),
			PIIPatterns:       []PIIPattern{},
		},
		Performance: PerformanceConfig{
			RateLimit:     c.getEnvInt("RATE_LIMIT", 1000),
			BufferSize:    c.getEnvInt("BUFFER_SIZE", 100),
			FlushInterval: time.Duration(c.getEnvInt("FLUSH_INTERVAL", 5)) * time.Second,
		},
		EnvConfig: EnvironmentConfig{
			DefaultLevel:     ParseLogLevel(c.getEnvString("DEFAULT_LEVEL", "INFO")),
			ProductionLevel:  ParseLogLevel(c.getEnvString("PRODUCTION_LEVEL", "WARN")),
			DevelopmentLevel: ParseLogLevel(c.getEnvString("DEVELOPMENT_LEVEL", "DEBUG")),
		},
	}

	return config, nil
}

// MergeConfigurations merges multiple configurations (later configs override earlier ones)
func (c *ConfigLoader) MergeConfigurations(configs ...*HaloLogConfig) (*HaloLogConfig, error) {
	if len(configs) == 0 {
		return nil, fmt.Errorf("no configurations provided")
	}

	result := configs[0]
	for i := 1; i < len(configs); i++ {
		result = c.mergeHaloLogConfig(result, configs[i])
	}

	return result, nil
}

// mergeHaloLogConfig merges two HaloLogConfig configurations
func (c *ConfigLoader) mergeHaloLogConfig(base, override *HaloLogConfig) *HaloLogConfig {
	if override == nil {
		return base
	}

	result := *base

	// Merge loggers
	if len(override.Loggers) > 0 {
		result.Loggers = override.Loggers
	}

	// Merge security
	if len(override.Security.PIIPatterns) > 0 {
		result.Security.PIIPatterns = override.Security.PIIPatterns
	}
	if override.Security.MaskSensitiveData {
		result.Security.MaskSensitiveData = override.Security.MaskSensitiveData
	}

	// Merge performance
	if override.Performance.RateLimit > 0 {
		result.Performance.RateLimit = override.Performance.RateLimit
	}
	if override.Performance.BufferSize > 0 {
		result.Performance.BufferSize = override.Performance.BufferSize
	}
	if override.Performance.FlushInterval > 0 {
		result.Performance.FlushInterval = override.Performance.FlushInterval
	}

	// Merge environment
	if override.EnvConfig.DefaultLevel != types.InfoLevel { // Default level is never zero
		result.EnvConfig.DefaultLevel = override.EnvConfig.DefaultLevel
	}
	if override.EnvConfig.ProductionLevel != types.InfoLevel { // Default level is never zero
		result.EnvConfig.ProductionLevel = override.EnvConfig.ProductionLevel
	}
	if override.EnvConfig.DevelopmentLevel != types.InfoLevel { // Default level is never zero
		result.EnvConfig.DevelopmentLevel = override.EnvConfig.DevelopmentLevel
	}

	// Merge startup registration
	if len(override.StartupRegistration.CustomFields) > 0 {
		result.StartupRegistration.CustomFields = override.StartupRegistration.CustomFields
	}
	if len(override.StartupRegistration.SensitiveFields) > 0 {
		result.StartupRegistration.SensitiveFields = override.StartupRegistration.SensitiveFields
	}
	if len(override.StartupRegistration.FieldColors) > 0 {
		result.StartupRegistration.FieldColors = override.StartupRegistration.FieldColors
	}
	if override.StartupRegistration.OptimalBufferSize > 0 {
		result.StartupRegistration.OptimalBufferSize = override.StartupRegistration.OptimalBufferSize
	}

	return &result
}

// Environment variable helpers
func (c *ConfigLoader) getEnvString(key, defaultValue string) string {
	if value := os.Getenv(c.envPrefix + key); value != "" {
		return value
	}
	return defaultValue
}

func (c *ConfigLoader) getEnvBool(key string, defaultValue bool) bool {
	value := os.Getenv(c.envPrefix + key)
	switch strings.ToLower(value) {
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	default:
		return defaultValue
	}
}

func (c *ConfigLoader) getEnvInt(key string, defaultValue int) int {
	value := os.Getenv(c.envPrefix + key)
	if value == "" {
		return defaultValue
	}

	var result int
	if _, err := fmt.Sscanf(value, "%d", &result); err != nil {
		return defaultValue
	}
	return result
}

func (c *ConfigLoader) getEnvStringSlice(key string, defaultValue []string) []string {
	value := os.Getenv(c.envPrefix + key)
	if value == "" {
		return defaultValue
	}

	return strings.Split(value, ",")
}
