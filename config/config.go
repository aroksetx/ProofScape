package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Cookie represents a browser cookie to set
type Cookie struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Domain   string `json:"domain,omitempty"`
	Path     string `json:"path,omitempty"`
	Secure   bool   `json:"secure,omitempty"`
	HTTPOnly bool   `json:"httpOnly,omitempty"`
}

// LocalStorage represents a localStorage key-value pair to set
type LocalStorage struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// CookieProfile represents a named set of cookies and localStorage values
type CookieProfile struct {
	Name         string         `json:"name"`
	Cookies      []Cookie       `json:"cookies"`
	LocalStorage []LocalStorage `json:"localStorage,omitempty"`
}

// URLConfig represents configuration for a single URL to capture
type URLConfig struct {
	Name            string         `json:"name"`
	URL             string         `json:"url"`
	Viewports       []Viewport     `json:"viewports,omitempty"`
	Delay           int            `json:"delay,omitempty"` // Delay in milliseconds
	Cookies         []Cookie       `json:"cookies,omitempty"`
	LocalStorage    []LocalStorage `json:"localStorage,omitempty"`
	CookieProfileID string         `json:"cookieProfileId,omitempty"` // Reference to a cookie profile
}

// Viewport represents browser viewport dimensions
type Viewport struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// Config represents the application configuration
type Config struct {
	URLs             []URLConfig     `json:"urls"`
	URLList          []string        `json:"urlList,omitempty"` // Simple list of URLs
	DefaultViewports []Viewport      `json:"defaultViewports"`
	DefaultDelay     int             `json:"defaultDelay,omitempty"` // Default delay for urlList items
	DefaultCookies   []Cookie        `json:"defaultCookies,omitempty"`
	DefaultStorage   []LocalStorage  `json:"defaultStorage,omitempty"`
	CookieProfiles   []CookieProfile `json:"cookieProfiles,omitempty"` // Named cookie profiles
	OutputDir        string          `json:"outputDir"`
	FileFormat       string          `json:"fileFormat"`
	Quality          int             `json:"quality"`
	Concurrency      int             `json:"concurrency"`
	ChromeMode       string          `json:"-"` // Not parsed from JSON, set by command line
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
	// Process URLList if provided
	if len(config.URLList) > 0 {
		// Set default delay if not specified
		defaultDelay := 1000 // 1 second default
		if config.DefaultDelay > 0 {
			defaultDelay = config.DefaultDelay
		}

		// Convert URLList into URLConfig objects
		for _, url := range config.URLList {
			if url = strings.TrimSpace(url); url == "" {
				continue
			}

			domainName := extractDomain(url)
			config.URLs = append(config.URLs, URLConfig{
				Name:         domainName,
				URL:          url,
				Viewports:    []Viewport{},
				Delay:        defaultDelay,
				Cookies:      make([]Cookie, 0),
				LocalStorage: make([]LocalStorage, 0),
			})
		}
	}

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

	// Validate cookie profiles
	cookieProfileMap := make(map[string]CookieProfile)
	for _, profile := range config.CookieProfiles {
		if profile.Name == "" {
			return fmt.Errorf("cookie profile is missing name")
		}
		cookieProfileMap[profile.Name] = profile
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

		// Apply cookie profile if specified
		if config.URLs[i].CookieProfileID != "" {
			profile, exists := cookieProfileMap[config.URLs[i].CookieProfileID]
			if !exists {
				return fmt.Errorf("URL #%d references non-existent cookie profile: %s", i+1, config.URLs[i].CookieProfileID)
			}

			// Apply profile cookies if URL doesn't have its own
			if len(config.URLs[i].Cookies) == 0 {
				config.URLs[i].Cookies = make([]Cookie, len(profile.Cookies))
				copy(config.URLs[i].Cookies, profile.Cookies)
			}

			// Apply profile localStorage if URL doesn't have its own
			if len(config.URLs[i].LocalStorage) == 0 {
				config.URLs[i].LocalStorage = make([]LocalStorage, len(profile.LocalStorage))
				copy(config.URLs[i].LocalStorage, profile.LocalStorage)
			}
		} else {
			// Apply default cookies if no profile specified and the URL doesn't have its own
			if len(config.URLs[i].Cookies) == 0 && len(config.DefaultCookies) > 0 {
				config.URLs[i].Cookies = make([]Cookie, len(config.DefaultCookies))
				copy(config.URLs[i].Cookies, config.DefaultCookies)
			}

			// Apply default localStorage if no profile specified and the URL doesn't have its own
			if len(config.URLs[i].LocalStorage) == 0 && len(config.DefaultStorage) > 0 {
				config.URLs[i].LocalStorage = make([]LocalStorage, len(config.DefaultStorage))
				copy(config.URLs[i].LocalStorage, config.DefaultStorage)
			}
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

// extractDomain extracts a domain name from a URL for use as a default name
func extractDomain(url string) string {
	// Remove protocol if present
	if strings.HasPrefix(url, "http://") {
		url = url[7:]
	} else if strings.HasPrefix(url, "https://") {
		url = url[8:]
	}

	// Remove www. prefix if present
	if strings.HasPrefix(url, "www.") {
		url = url[4:]
	}

	// Get domain part (stop at first slash)
	if idx := strings.Index(url, "/"); idx > 0 {
		url = url[:idx]
	}

	// Remove port if present
	if idx := strings.Index(url, ":"); idx > 0 {
		url = url[:idx]
	}

	return url
}
