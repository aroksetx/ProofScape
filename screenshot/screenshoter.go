package screenshot

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"screenshot-tool/config"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/chromedp"
)

// findChromeExecutable attempts to locate the Chrome executable on the system
func findChromeExecutable() (string, error) {
	// Check for environment variable first
	if envPath := os.Getenv("CHROME_PATH"); envPath != "" {
		if _, err := os.Stat(envPath); err == nil {
			return envPath, nil
		}
	}

	// Common locations based on OS
	switch runtime.GOOS {
	case "darwin":
		paths := []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Google Chrome Canary.app/Contents/MacOS/Google Chrome Canary",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		}
		for _, path := range paths {
			if _, err := os.Stat(path); err == nil {
				return path, nil
			}
		}
	case "windows":
		paths := []string{
			filepath.Join(os.Getenv("ProgramFiles"), "Google/Chrome/Application/chrome.exe"),
			filepath.Join(os.Getenv("ProgramFiles(x86)"), "Google/Chrome/Application/chrome.exe"),
			filepath.Join(os.Getenv("LocalAppData"), "Google/Chrome/Application/chrome.exe"),
		}
		for _, path := range paths {
			if _, err := os.Stat(path); err == nil {
				return path, nil
			}
		}
	case "linux":
		paths := []string{
			"/usr/bin/google-chrome",
			"/usr/bin/chromium",
			"/usr/bin/chromium-browser",
			"/snap/bin/chromium",
		}
		for _, path := range paths {
			if _, err := os.Stat(path); err == nil {
				return path, nil
			}
		}
	}

	// Try finding in PATH
	if path, err := exec.LookPath("google-chrome"); err == nil {
		return path, nil
	}
	if path, err := exec.LookPath("chromium"); err == nil {
		return path, nil
	}
	if path, err := exec.LookPath("chromium-browser"); err == nil {
		return path, nil
	}

	return "", fmt.Errorf("could not find Chrome executable")
}

// startDockerChrome starts a Chrome instance in Docker if not already running
func startDockerChrome() (string, error) {
	// Check if docker is installed
	if _, err := exec.LookPath("docker"); err != nil {
		return "", fmt.Errorf("docker not installed: %w", err)
	}

	// Check if chrome container is already running
	cmd := exec.Command("docker", "ps", "-q", "-f", "name=chrome", "-f", "status=running")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to check for running chrome container: %w", err)
	}

	// If container is already running, return its address
	if len(output) > 0 {
		log.Printf("Using existing Chrome container")
		return "http://localhost:9222", nil
	}

	// Start a new chrome container
	log.Printf("Starting Chrome container...")
	cmd = exec.Command("docker", "run", "-d", "--rm", "--name", "chrome", "-p", "9222:9222", "browserless/chrome")
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("failed to start chrome container: %w, output: %s", err, string(output))
	}

	// Wait for container to be ready
	log.Printf("Waiting for Chrome container to be ready...")
	time.Sleep(3 * time.Second)

	// Verify chrome is available
	maxRetries := 5
	for i := 0; i < maxRetries; i++ {
		cmd = exec.Command("curl", "-s", "http://localhost:9222/json/version")
		if output, err := cmd.CombinedOutput(); err == nil && strings.Contains(string(output), "webSocketDebuggerUrl") {
			log.Printf("Chrome container is ready")
			return "http://localhost:9222", nil
		}
		time.Sleep(1 * time.Second)
	}

	return "", fmt.Errorf("chrome container started but not responding")
}

// Screenshoter handles the screenshot capturing logic
type Screenshoter struct {
	Config *config.Config
}

// NewScreenshoter creates a new Screenshoter
func NewScreenshoter(cfg *config.Config) *Screenshoter {
	return &Screenshoter{
		Config: cfg,
	}
}

// CaptureURL captures screenshots for a given URL
func (s *Screenshoter) CaptureURL(ctx context.Context, urlConfig config.URLConfig) error {
	// Create context with timeout
	timeoutCtx, cancel := context.WithTimeout(ctx, time.Duration(urlConfig.Delay+30000)*time.Millisecond)
	defer cancel()

	// Create browser options
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.WindowSize(urlConfig.Viewport.Width, urlConfig.Viewport.Height),
		chromedp.DisableGPU,
		chromedp.NoSandbox,
		chromedp.Headless,
	)

	// Define context variables here
	var allocCtx context.Context
	var browserCtx context.Context
	var cancelAlloc context.CancelFunc
	var cancelBrowser context.CancelFunc

	// Determine which Chrome implementation to use
	// Priority: 1. Local Chrome, 2. Docker Chrome
	// Try local Chrome first
	if execPath, err := findChromeExecutable(); err == nil {
		// Use local Chrome executable
		log.Printf("Using local Chrome executable at: %s", execPath)
		opts = append(opts, chromedp.ExecPath(execPath))

		// Create allocator context with local Chrome
		allocCtx, cancelAlloc = chromedp.NewExecAllocator(timeoutCtx, opts...)
		defer cancelAlloc()
	} else {
		// Try Docker Chrome as fallback
		log.Printf("Local Chrome not found: %v", err)
		log.Printf("Attempting to use Docker Chrome...")

		if dockerURL, err := startDockerChrome(); err == nil {
			// Use Docker Chrome
			log.Printf("Using Docker Chrome at: %s", dockerURL)
			allocCtx, cancelAlloc = chromedp.NewRemoteAllocator(timeoutCtx, dockerURL)
			defer cancelAlloc()
		} else {
			// Fallback to default Chrome as last resort
			log.Printf("Docker Chrome failed: %v", err)
			log.Printf("Falling back to default Chrome settings")

			allocCtx, cancelAlloc = chromedp.NewExecAllocator(timeoutCtx, opts...)
			defer cancelAlloc()
		}
	}

	// Create browser context
	browserCtx, cancelBrowser = chromedp.NewContext(allocCtx, chromedp.WithLogf(log.Printf))
	defer cancelBrowser()

	// Create directory for this URL
	urlDir := filepath.Join(s.Config.OutputDir, sanitizeFilename(urlConfig.Name))
	if err := os.MkdirAll(urlDir, 0755); err != nil {
		return fmt.Errorf("failed to create directory for URL %s: %w", urlConfig.Name, err)
	}

	// Capture full page screenshot
	if err := s.captureFullPageScreenshot(browserCtx, urlConfig, urlDir); err != nil {
		return fmt.Errorf("failed to capture full page screenshot for %s: %w", urlConfig.Name, err)
	}

	// Capture viewport screenshots
	if err := s.captureViewportScreenshots(browserCtx, urlConfig, urlDir); err != nil {
		return fmt.Errorf("failed to capture viewport screenshots for %s: %w", urlConfig.Name, err)
	}

	return nil
}

// captureFullPageScreenshot captures a full page screenshot
func (s *Screenshoter) captureFullPageScreenshot(ctx context.Context, urlConfig config.URLConfig, urlDir string) error {
	var buf []byte
	timestamp := time.Now().Format("20060102-150405")
	filename := fmt.Sprintf("%s-full.%s", timestamp, s.Config.FileFormat)
	filepath := filepath.Join(urlDir, filename)

	// Create actions to navigate and capture screenshot
	tasks := []chromedp.Action{
		chromedp.Navigate(urlConfig.URL),
		chromedp.Sleep(time.Duration(urlConfig.Delay) * time.Millisecond),

		// Scroll to bottom to load lazy content
		chromedp.Evaluate(`window.scrollTo(0, document.body.scrollHeight)`, nil),
		chromedp.Sleep(1 * time.Second), // Wait for content to load

		// Scroll back to top
		chromedp.Evaluate(`window.scrollTo(0, 0)`, nil),
		chromedp.Sleep(1 * time.Second), // Wait for animations to complete

		// Add additional delay to ensure all elements are loaded
		chromedp.Sleep(2 * time.Second),
		chromedp.ActionFunc(func(ctx context.Context) error {
			// Get page metrics
			var metrics map[string]interface{}
			if err := chromedp.Evaluate(`({
				width: Math.max(document.body.scrollWidth, document.documentElement.scrollWidth),
				height: Math.max(document.body.scrollHeight, document.documentElement.scrollHeight),
			})`, &metrics).Do(ctx); err != nil {
				return err
			}

			// Set viewport to full page size
			width := int64(metrics["width"].(float64))
			height := int64(metrics["height"].(float64))
			if err := emulation.SetDeviceMetricsOverride(width, height, 1, false).Do(ctx); err != nil {
				return err
			}

			// Capture full screenshot
			return chromedp.CaptureScreenshot(&buf).Do(ctx)
		}),
	}

	// Execute tasks
	if err := chromedp.Run(ctx, tasks...); err != nil {
		return err
	}

	// Save screenshot to file
	if err := os.WriteFile(filepath, buf, 0644); err != nil {
		return err
	}

	log.Printf("Captured full page screenshot for %s: %s", urlConfig.Name, filepath)
	return nil
}

// captureViewportScreenshots captures screenshots divided by viewport
func (s *Screenshoter) captureViewportScreenshots(ctx context.Context, urlConfig config.URLConfig, urlDir string) error {
	var pageHeight float64
	timestamp := time.Now().Format("20060102-150405")

	// First navigate and get page height
	if err := chromedp.Run(ctx,
		chromedp.Navigate(urlConfig.URL),
		chromedp.Sleep(time.Duration(urlConfig.Delay)*time.Millisecond),

		// Scroll to bottom to load lazy content
		chromedp.Evaluate(`window.scrollTo(0, document.body.scrollHeight)`, nil),
		chromedp.Sleep(1*time.Second), // Wait for content to load

		// Scroll back to top
		chromedp.Evaluate(`window.scrollTo(0, 0)`, nil),
		chromedp.Sleep(1*time.Second), // Wait for animations to complete

		chromedp.Evaluate(`Math.max(document.body.scrollHeight, document.documentElement.scrollHeight)`, &pageHeight),
	); err != nil {
		return err
	}

	// Calculate how many viewport sections we need
	viewportHeight := float64(urlConfig.Viewport.Height)
	viewportCount := int(pageHeight / viewportHeight)
	if float64(viewportCount)*viewportHeight < pageHeight {
		viewportCount++
	}

	// Capture each viewport section
	for i := 0; i < viewportCount; i++ {
		var buf []byte
		scrollPos := float64(i) * viewportHeight
		filename := fmt.Sprintf("%s-viewport-%dx%d-%d.%s", timestamp, urlConfig.Viewport.Width, urlConfig.Viewport.Height, i+1, s.Config.FileFormat)
		filepath := filepath.Join(urlDir, filename)

		// Scroll to position and capture screenshot of only the viewport
		if err := chromedp.Run(ctx,
			// Scroll to position
			chromedp.Evaluate(fmt.Sprintf(`window.scrollTo(0, %f)`, scrollPos), nil),
			chromedp.Sleep(500*time.Millisecond), // Give time for any animations to complete

			// Ensure device metrics are set to capture only viewport
			emulation.SetDeviceMetricsOverride(int64(urlConfig.Viewport.Width), int64(urlConfig.Viewport.Height), 1, false).
				WithScreenOrientation(&emulation.ScreenOrientation{
					Type:  emulation.OrientationTypePortraitPrimary,
					Angle: 0,
				}),

			// Capture only the viewport screenshot
			chromedp.CaptureScreenshot(&buf),
		); err != nil {
			return err
		}

		// Save screenshot to file
		if err := os.WriteFile(filepath, buf, 0644); err != nil {
			return err
		}

		log.Printf("Captured viewport screenshot %d/%d for %s: %s", i+1, viewportCount, urlConfig.Name, filepath)
	}

	return nil
}

// CaptureURLs captures screenshots for all URLs in configuration
func (s *Screenshoter) CaptureURLs(ctx context.Context) error {
	// Create semaphore to limit concurrency
	sem := make(chan struct{}, s.Config.Concurrency)
	errChan := make(chan error, len(s.Config.URLs))
	doneChan := make(chan struct{}, len(s.Config.URLs))

	// Process each URL
	for _, urlConfig := range s.Config.URLs {
		urlConfig := urlConfig // Create local copy for goroutine

		// Acquire semaphore
		sem <- struct{}{}

		// Start goroutine to process URL
		go func() {
			defer func() {
				// Release semaphore when done
				<-sem
				doneChan <- struct{}{}
			}()

			// Capture URL
			if err := s.CaptureURL(ctx, urlConfig); err != nil {
				errChan <- fmt.Errorf("error capturing URL %s: %w", urlConfig.Name, err)
			}
		}()
	}

	// Wait for all goroutines to complete
	for i := 0; i < len(s.Config.URLs); i++ {
		<-doneChan
	}

	// Check if there were any errors
	select {
	case err := <-errChan:
		return err
	default:
		return nil
	}
}
