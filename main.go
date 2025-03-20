package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"screenshot-tool/config"
	"screenshot-tool/screenshot"
)

// cleanupDockerContainer stops the chrome docker container if it was started by this app
func cleanupDockerContainer() {
	// Check if docker is installed
	if _, err := exec.LookPath("docker"); err != nil {
		return
	}

	// Check if chrome container is running
	cmd := exec.Command("docker", "ps", "-q", "-f", "name=chrome", "-f", "status=running")
	output, err := cmd.Output()
	if err != nil || len(output) == 0 {
		return
	}

	log.Println("Stopping Chrome Docker container...")
	cmd = exec.Command("docker", "stop", "chrome")
	if err := cmd.Run(); err != nil {
		log.Printf("Failed to stop Chrome container: %v", err)
		return
	}
	log.Println("Chrome Docker container stopped")
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

func main() {
	// Parse command line flags
	configPath := flag.String("config", "config.json", "Path to configuration file")
	cmdUrls := flag.String("urls", "", "Comma-separated list of URLs to capture (overrides config file URLs)")
	cmdUrl := flag.String("url", "", "Single URL to capture (overrides config file URLs)")
	name := flag.String("name", "", "Name for the URL when using -url flag (defaults to domain)")
	delay := flag.Int("delay", 0, "Delay in milliseconds for page loading when using -url flag (defaults to 1000)")
	flag.Parse()

	// Load configuration
	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Handle command-line URLs if provided
	if *cmdUrl != "" || *cmdUrls != "" {
		// Override config URLs with command line URLs
		cfg.URLs = []config.URLConfig{}

		if *cmdUrl != "" {
			// Single URL mode
			urlName := *name
			if urlName == "" {
				// Extract domain as default name
				urlName = extractDomain(*cmdUrl)
			}

			urlDelay := 1000
			if *delay > 0 {
				urlDelay = *delay
			}

			cfg.URLs = append(cfg.URLs, config.URLConfig{
				Name:      urlName,
				URL:       *cmdUrl,
				Viewports: []config.Viewport{},
				Delay:     urlDelay,
			})

			log.Printf("Using single URL from command line: %s", *cmdUrl)
		} else if *cmdUrls != "" {
			// Multiple URLs mode
			urlList := strings.Split(*cmdUrls, ",")
			for _, url := range urlList {
				url = strings.TrimSpace(url)
				if url == "" {
					continue
				}

				urlDelay := 1000
				if *delay > 0 {
					urlDelay = *delay
				}

				cfg.URLs = append(cfg.URLs, config.URLConfig{
					Name:      extractDomain(url),
					URL:       url,
					Viewports: []config.Viewport{},
					Delay:     urlDelay,
				})
			}

			log.Printf("Using %d URLs from command line", len(cfg.URLs))
		}
	}

	// Check if we have any URLs to process
	if len(cfg.URLs) == 0 {
		log.Fatalf("No URLs to process. Please specify URLs in the config file or use -url/-urls flags.")
	}

	// Create screenshot handler
	screenshoter := screenshot.NewScreenshoter(cfg)

	// Create context with cancel for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Set up signal handling for graceful shutdown
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		sig := <-signalChan
		log.Printf("Received signal: %v, shutting down gracefully", sig)
		cancel()
		cleanupDockerContainer()
		// Allow some time for cleanup then exit if it takes too long
		time.Sleep(5 * time.Second)
		os.Exit(1)
	}()

	// Run screenshot capture
	log.Printf("Starting screenshot capture for %d URLs", len(cfg.URLs))
	startTime := time.Now()

	// Capture screenshots
	if err := screenshoter.CaptureURLs(ctx); err != nil {
		log.Printf("Screenshot capture failed: %v", err)
		cleanupDockerContainer()
		os.Exit(1)
	}

	// Log completion time
	elapsed := time.Since(startTime)
	log.Printf("Screenshot capture completed successfully in %v", elapsed)

	// Cleanup
	cleanupDockerContainer()
}
