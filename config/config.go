package config

import (
	"encoding/json"
	"fmt"
	"os"
)

// URLConfig represents configuration for a single URL to capture
type URLConfig struct {
	Name      string     `json:"name"`
	URL       string     `json:"url"`
	Viewports []Viewport `json:"viewports,omitempty"`
	Delay     int        `json:"delay,omitempty"` // Delay in milliseconds
}

// Viewport represents browser viewport dimensions
type Viewport struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// Config represents the application configuration
type Config struct {
	URLs             []URLConfig `json:"urls"`
	DefaultViewports []Viewport  `json:"defaultViewports"`
	OutputDir        string      `json:"outputDir"`
	FileFormat       string      `json:"fileFormat"`
	Quality          int         `json:"quality"`
	Concurrency      int         `json:"concurrency"`
}

// LoadConfig loads configuration from a file
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("error reading config file: %w", err)
	}

	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("error parsing config file: %w", err)
	}

	// Validate and set defaults
	if err := validateConfig(&config); err != nil {
		return nil, err
	}

	// Ensure output directory exists
	if err := ensureOutputDir(config.OutputDir); err != nil {
		return nil, err
	}

	return &config, nil
}

// validateConfig validates configuration and sets defaults
func validateConfig(config *Config) error {
	// Check if there are any URLs to process
	if len(config.URLs) == 0 {
		return fmt.Errorf("no URLs specified in configuration")
	}

	// Set default viewports if not specified or empty
	if len(config.DefaultViewports) == 0 {
		// Set default common viewport sizes (desktop, tablet, mobile)
		config.DefaultViewports = []Viewport{
			{Width: 1920, Height: 1080}, // Desktop large
			{Width: 1366, Height: 768},  // Desktop common
			{Width: 768, Height: 1024},  // Tablet portrait
			{Width: 375, Height: 667},   // Mobile (iPhone)
		}
	}

	// Set default output directory if not specified
	if config.OutputDir == "" {
		config.OutputDir = "./screenshots"
	}

	// Set default file format if not specified
	if config.FileFormat == "" {
		config.FileFormat = "png"
	} else if config.FileFormat != "png" && config.FileFormat != "jpeg" {
		return fmt.Errorf("unsupported file format: %s (supported: png, jpeg)", config.FileFormat)
	}

	// Set default quality if not specified
	if config.Quality == 0 {
		config.Quality = 80
	} else if config.Quality < 1 || config.Quality > 100 {
		return fmt.Errorf("quality must be between 1 and 100")
	}

	// Set default concurrency if not specified
	if config.Concurrency == 0 {
		config.Concurrency = 2
	} else if config.Concurrency < 1 {
		return fmt.Errorf("concurrency must be at least 1")
	}

	// Validate and set defaults for each URL
	for i := range config.URLs {
		// Ensure URL has a name
		if config.URLs[i].Name == "" {
			config.URLs[i].Name = fmt.Sprintf("page-%d", i+1)
		}

		// Ensure URL has a value
		if config.URLs[i].URL == "" {
			return fmt.Errorf("URL #%d is missing URL value", i+1)
		}

		// If no viewports specified for this URL, use the default viewports
		if len(config.URLs[i].Viewports) == 0 {
			config.URLs[i].Viewports = make([]Viewport, len(config.DefaultViewports))
			copy(config.URLs[i].Viewports, config.DefaultViewports)
		}

		// Set default delay if not specified
		if config.URLs[i].Delay == 0 {
			config.URLs[i].Delay = 1000 // 1 second default
		}
	}

	return nil
}

// ensureOutputDir ensures the output directory exists
func ensureOutputDir(dir string) error {
	return os.MkdirAll(dir, 0755)
}
