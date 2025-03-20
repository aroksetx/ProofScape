package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/exec"
	"os/signal"
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

func main() {
	// Parse command line flags
	configPath := flag.String("config", "config.json", "Path to configuration file")
	flag.Parse()

	// Load configuration
	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
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
