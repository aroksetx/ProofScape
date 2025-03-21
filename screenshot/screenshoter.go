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

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/storage"
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

		// Verify the existing container responds before continuing
		if err := checkChromeResponseFromContainer(5); err != nil {
			// Container exists but doesn't respond, stop and remove it
			log.Printf("Existing Chrome container not responding, removing it: %v", err)
			stopCmd := exec.Command("docker", "rm", "-f", "chrome")
			stopCmd.Run() // Ignore errors, we'll try to recreate
		} else {
			return "http://localhost:9222", nil
		}
	}

	// Start a new chrome container with improved configuration
	log.Printf("Starting Chrome container...")
	cmd = exec.Command("docker", "run", "-d", "--rm", "--name", "chrome",
		"-p", "9222:9222", // Using standard port 9222 for chromedp/headless-shell
		"--cap-add=SYS_ADMIN",              // Add capabilities needed for Chrome
		"chromedp/headless-shell:latest",   // Use chromedp's official headless shell image
		"--disable-web-security",           // Disable web security for testing
		"--ignore-certificate-errors",      // Ignore SSL certificate errors
		"--allow-running-insecure-content") // Allow loading insecure content

	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("failed to start chrome container: %w, output: %s", err, string(output))
	}

	// Wait for container to be ready with increased timeout
	log.Printf("Waiting for Chrome container to be ready (this may take up to 20 seconds)...")

	// Check if Chrome responds within timeout
	if err := checkChromeResponseFromContainer(20); err != nil {
		// Get container logs for diagnostics
		logsCmd := exec.Command("docker", "logs", "chrome")
		logs, _ := logsCmd.CombinedOutput()

		// Stop the container since it's not working
		stopCmd := exec.Command("docker", "rm", "-f", "chrome")
		stopCmd.Run() // Ignore errors

		return "", fmt.Errorf("chrome container started but not responding: %v\nContainer logs: %s",
			err, string(logs))
	}

	log.Printf("Chrome container is ready")
	return "http://localhost:9222", nil
}

// checkChromeResponseFromContainer checks if Chrome is responding in the container
// with the specified timeout in seconds
func checkChromeResponseFromContainer(timeoutSeconds int) error {
	// Try multiple times with increasing delay
	maxRetries := timeoutSeconds
	baseDelay := 1 * time.Second

	for i := 0; i < maxRetries; i++ {
		// Try standard Chrome endpoint first
		cmd := exec.Command("curl", "-s", "--max-time", "2", "http://localhost:9222/json/version")
		output, err := cmd.CombinedOutput()

		if err == nil && strings.Contains(string(output), "webSocketDebuggerUrl") {
			// Chrome is responding properly
			return nil
		}

		// Try browserless endpoint which might be different
		cmd = exec.Command("curl", "-s", "--max-time", "2", "http://localhost:9222/json")
		output, err = cmd.CombinedOutput()

		if err == nil && len(output) > 0 && (strings.Contains(string(output), "webSocketDebuggerUrl") ||
			strings.Contains(string(output), "browserless")) {
			// Browserless is responding
			return nil
		}

		// Increase delay slightly as we retry
		delay := baseDelay + time.Duration(i*150)*time.Millisecond
		log.Printf("Waiting for Chrome to be ready in container (attempt %d/%d)...", i+1, maxRetries)
		time.Sleep(delay)
	}

	return fmt.Errorf("timeout after %d seconds", timeoutSeconds)
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

// CaptureURL captures screenshots for a given URL with all configured viewports
func (s *Screenshoter) CaptureURL(ctx context.Context, urlConfig config.URLConfig) error {
	// Create context with timeout - increase for complex pages
	// Calculate a longer timeout based on the number of viewports and complexity
	viewportsCount := len(urlConfig.Viewports)
	timeoutDuration := time.Duration(urlConfig.Delay*3+60000*viewportsCount) * time.Millisecond
	timeoutCtx, cancel := context.WithTimeout(ctx, timeoutDuration)
	defer cancel()

	log.Printf("Set timeout of %v for URL %s with %d viewports", timeoutDuration, urlConfig.Name, viewportsCount)

	// Create directory for this URL
	urlDir := filepath.Join(s.Config.OutputDir, sanitizeFilename(urlConfig.Name))
	if err := os.MkdirAll(urlDir, 0755); err != nil {
		return fmt.Errorf("failed to create directory for URL %s: %w", urlConfig.Name, err)
	}

	// Process each viewport for this URL
	for _, viewport := range urlConfig.Viewports {
		log.Printf("Capturing screenshots for %s at viewport %dx%d", urlConfig.Name, viewport.Width, viewport.Height)
		if err := s.captureWithViewport(timeoutCtx, urlConfig, viewport, urlDir, true); err != nil {
			return fmt.Errorf("failed to capture screenshots for %s at viewport %dx%d: %w",
				urlConfig.Name, viewport.Width, viewport.Height, err)
		}
	}

	return nil
}

// captureWithViewport captures screenshots for a specific viewport size
func (s *Screenshoter) captureWithViewport(ctx context.Context, urlConfig config.URLConfig, viewport config.Viewport, urlDir string, captureViewports bool) error {
	// Create browser options
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.WindowSize(viewport.Width, viewport.Height),
		chromedp.DisableGPU,
		chromedp.NoSandbox,
		chromedp.Headless,
		chromedp.Flag("ignore-certificate-errors", true),
	)

	// Define context variables here
	var allocCtx context.Context
	var browserCtx context.Context
	var cancelAlloc context.CancelFunc
	var cancelBrowser context.CancelFunc

	// Determine which Chrome implementation to use based on the specified mode
	switch s.Config.ChromeMode {
	case "local":
		// Force use of local Chrome
		if execPath, err := findChromeExecutable(); err == nil {
			// Use local Chrome executable
			log.Printf("Using local Chrome executable at: %s", execPath)
			opts = append(opts, chromedp.ExecPath(execPath))

			// Create allocator context with local Chrome
			allocCtx, cancelAlloc = chromedp.NewExecAllocator(ctx, opts...)
			defer cancelAlloc()
		} else {
			return fmt.Errorf("local Chrome mode specified but Chrome executable not found: %v", err)
		}

	case "docker":
		// Force use of Docker Chrome
		log.Printf("Docker Chrome mode specified, starting or connecting to Docker Chrome...")
		if dockerURL, err := startDockerChrome(); err == nil {
			// Use Docker Chrome
			log.Printf("Using Docker Chrome at: %s", dockerURL)
			// Use standard Chrome debugging protocol with chromedp/headless-shell
			allocCtx, cancelAlloc = chromedp.NewRemoteAllocator(ctx, dockerURL)
			defer cancelAlloc()
		} else {
			return fmt.Errorf("docker Chrome mode specified but failed to start or connect to Docker Chrome: %v", err)
		}

	default: // "auto" mode - try local, then Docker, then fallback
		// Try local Chrome first
		if execPath, err := findChromeExecutable(); err == nil {
			// Use local Chrome executable
			log.Printf("Using local Chrome executable at: %s", execPath)
			opts = append(opts, chromedp.ExecPath(execPath))

			// Create allocator context with local Chrome
			allocCtx, cancelAlloc = chromedp.NewExecAllocator(ctx, opts...)
			defer cancelAlloc()
		} else {
			// Try Docker Chrome as fallback
			log.Printf("Local Chrome not found: %v", err)
			log.Printf("Attempting to use Docker Chrome...")

			if dockerURL, err := startDockerChrome(); err == nil {
				// Use Docker Chrome
				log.Printf("Using Docker Chrome at: %s", dockerURL)
				// Use standard Chrome debugging protocol with chromedp/headless-shell
				allocCtx, cancelAlloc = chromedp.NewRemoteAllocator(ctx, dockerURL)
				defer cancelAlloc()
			} else {
				// Fallback to default Chrome as last resort
				log.Printf("Docker Chrome failed: %v", err)
				log.Printf("Falling back to default Chrome settings")

				allocCtx, cancelAlloc = chromedp.NewExecAllocator(ctx, opts...)
				defer cancelAlloc()
			}
		}
	}

	// Create browser context
	browserCtx, cancelBrowser = chromedp.NewContext(allocCtx, chromedp.WithLogf(log.Printf))
	defer cancelBrowser()

	// Capture full page screenshot
	if err := s.captureFullPageScreenshot(browserCtx, urlConfig, viewport, urlDir); err != nil {
		return fmt.Errorf("failed to capture full page screenshot for %s at viewport %dx%d: %w",
			urlConfig.Name, viewport.Width, viewport.Height, err)
	}

	// Capture viewport screenshots if requested
	if captureViewports {
		if err := s.captureViewportScreenshots(browserCtx, urlConfig, viewport, urlDir); err != nil {
			return fmt.Errorf("failed to capture viewport screenshots for %s at viewport %dx%d: %w",
				urlConfig.Name, viewport.Width, viewport.Height, err)
		}
	}

	return nil
}

// SaveCookiesToFile saves all current cookies to a log file
func SaveCookiesToFile(ctx context.Context, urlConfig config.URLConfig, stage string, urlDir string, viewport config.Viewport, screenshotType string) chromedp.ActionFunc {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		log.Printf("SaveCookiesToFile called for %s (stage: %s, type: %s)", urlConfig.Name, stage, screenshotType)

		// Get all cookies
		cookies, err := storage.GetCookies().Do(ctx)
		if err != nil {
			log.Printf("ERROR: Failed to get cookies: %v", err)
			return err
		}
		log.Printf("Retrieved %d cookies for %s", len(cookies), urlConfig.Name)

		// Create a single log file for the URL
		timestamp := time.Now().Format("2006-01-02 15:04:05.000")

		// Save text log
		if err := saveCookiesTextLog(cookies, urlConfig, stage, urlDir, viewport, screenshotType, timestamp); err != nil {
			log.Printf("ERROR: Failed to save cookies text log: %v", err)
			return err
		}
		log.Printf("Saved cookies to text log successfully")

		// Save CSV log
		if err := saveCookiesCSV(cookies, urlConfig, stage, urlDir, viewport, screenshotType, timestamp); err != nil {
			log.Printf("ERROR: Failed to save cookies CSV: %v", err)
			return err
		}
		log.Printf("Saved cookies to CSV successfully")

		log.Printf("Saved %d cookies to log files (viewport: %dx%d, type: %s, stage: %s)",
			len(cookies), viewport.Width, viewport.Height, screenshotType, stage)
		return nil
	})
}

// saveCookiesTextLog saves cookies in text format
func saveCookiesTextLog(cookies []*network.Cookie, urlConfig config.URLConfig, stage string, urlDir string, viewport config.Viewport, screenshotType, timestamp string) error {
	// Use a fixed filename for each URL
	filename := fmt.Sprintf("%s-cookies.log", sanitizeFilename(urlConfig.Name))
	filepath := filepath.Join(urlDir, filename)

	// Format cookies as text
	var cookieText strings.Builder
	cookieText.WriteString(fmt.Sprintf("\n\n========== %s ==========\n", stage))
	cookieText.WriteString(fmt.Sprintf("URL: %s (%s)\n", urlConfig.Name, urlConfig.URL))
	cookieText.WriteString(fmt.Sprintf("Timestamp: %s\n", timestamp))
	cookieText.WriteString(fmt.Sprintf("Viewport: %dx%d\n", viewport.Width, viewport.Height))
	cookieText.WriteString(fmt.Sprintf("Screenshot Type: %s\n", screenshotType))
	cookieText.WriteString(fmt.Sprintf("Step: %s\n", stage))

	// Add information about configured cookies if we're in the "before" stage
	if strings.Contains(stage, "before") && len(urlConfig.Cookies) > 0 {
		cookieText.WriteString("\nConfigured cookies that will be set:\n")
		for i, cookie := range urlConfig.Cookies {
			cookieText.WriteString(fmt.Sprintf("  Config Cookie #%d: %s=%s (domain: %s, path: %s)\n",
				i+1, cookie.Name, cookie.Value,
				cookie.Domain, cookie.Path))
		}
	}

	cookieText.WriteString("\n----------------------------------------\n")
	cookieText.WriteString(fmt.Sprintf("Current cookies (%d):\n", len(cookies)))

	for i, cookie := range cookies {
		cookieText.WriteString(fmt.Sprintf("Cookie #%d:\n", i+1))
		cookieText.WriteString(fmt.Sprintf("  Name: %s\n", cookie.Name))
		cookieText.WriteString(fmt.Sprintf("  Value: %s\n", cookie.Value))
		cookieText.WriteString(fmt.Sprintf("  Domain: %s\n", cookie.Domain))
		cookieText.WriteString(fmt.Sprintf("  Path: %s\n", cookie.Path))
		cookieText.WriteString(fmt.Sprintf("  Expires: %s\n", time.Unix(int64(cookie.Expires), 0)))
		cookieText.WriteString(fmt.Sprintf("  Size: %d\n", cookie.Size))
		cookieText.WriteString(fmt.Sprintf("  HttpOnly: %t\n", cookie.HTTPOnly))
		cookieText.WriteString(fmt.Sprintf("  Secure: %t\n", cookie.Secure))
		cookieText.WriteString(fmt.Sprintf("  Session: %t\n", cookie.Session))
		cookieText.WriteString(fmt.Sprintf("  SameSite: %s\n", cookie.SameSite))
		cookieText.WriteString(fmt.Sprintf("  Priority: %s\n", cookie.Priority))
		cookieText.WriteString("----------------------------------------\n")
	}

	// Check if file exists and append to it
	var fileContent []byte
	if _, err := os.Stat(filepath); err == nil {
		// File exists, read existing content
		fileContent, err = os.ReadFile(filepath)
		if err != nil {
			return err
		}
	}

	// Append new content
	fileContent = append(fileContent, []byte(cookieText.String())...)

	// Write to file
	if err := os.WriteFile(filepath, fileContent, 0644); err != nil {
		return err
	}

	return nil
}

// saveCookiesCSV saves cookies in CSV format
func saveCookiesCSV(cookies []*network.Cookie, urlConfig config.URLConfig, stage string, urlDir string, viewport config.Viewport, screenshotType, timestamp string) error {
	// Use a fixed filename for each URL
	filename := fmt.Sprintf("%s-cookies.csv", sanitizeFilename(urlConfig.Name))
	filepath := filepath.Join(urlDir, filename)

	log.Printf("Saving cookies to CSV file: %s", filepath)

	// Check if file exists and determine if we need to write headers
	writeHeader := true
	if _, err := os.Stat(filepath); err == nil {
		writeHeader = false
		log.Printf("CSV file exists, appending without headers")
	} else {
		log.Printf("CSV file does not exist, will create with headers")
	}

	// Open file for appending
	file, err := os.OpenFile(filepath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("ERROR: Failed to open CSV file: %v", err)
		return err
	}
	defer file.Close()

	// Write header if needed
	if writeHeader {
		headerLine := "Timestamp,URL,URL_Name,Stage,Screenshot_Type,Viewport,Cookie_Name,Cookie_Value,Domain,Path,Expires,Size,HttpOnly,Secure,Session,SameSite,Priority\n"
		if _, err := file.WriteString(headerLine); err != nil {
			log.Printf("ERROR: Failed to write CSV header: %v", err)
			return err
		}
		log.Printf("Wrote CSV headers")
	}

	// Write cookie records
	log.Printf("Writing %d cookies to CSV", len(cookies))
	for _, cookie := range cookies {
		// Escape fields that might contain commas
		urlValue := strings.ReplaceAll(urlConfig.URL, ",", "\\,")
		urlName := strings.ReplaceAll(urlConfig.Name, ",", "\\,")
		cookieName := strings.ReplaceAll(cookie.Name, ",", "\\,")
		cookieValue := strings.ReplaceAll(cookie.Value, ",", "\\,")
		cookieDomain := strings.ReplaceAll(cookie.Domain, ",", "\\,")
		cookiePath := strings.ReplaceAll(cookie.Path, ",", "\\,")

		// Format expiration date
		expiresStr := time.Unix(int64(cookie.Expires), 0).Format("2006-01-02 15:04:05")

		// Create CSV line
		line := fmt.Sprintf("%s,%s,%s,%s,%s,%dx%d,%s,%s,%s,%s,%s,%d,%t,%t,%t,%s,%s\n",
			timestamp,
			urlValue,
			urlName,
			stage,
			screenshotType,
			viewport.Width, viewport.Height,
			cookieName,
			cookieValue,
			cookieDomain,
			cookiePath,
			expiresStr,
			cookie.Size,
			cookie.HTTPOnly,
			cookie.Secure,
			cookie.Session,
			cookie.SameSite,
			cookie.Priority)

		if _, err := file.WriteString(line); err != nil {
			return err
		}
	}

	log.Printf("Successfully wrote cookies to CSV file")
	return nil
}

// captureFullPageScreenshot captures a full page screenshot
func (s *Screenshoter) captureFullPageScreenshot(ctx context.Context, urlConfig config.URLConfig, viewport config.Viewport, urlDir string) error {
	var buf []byte
	timestamp := time.Now().Format("20060102-150405")
	filename := fmt.Sprintf("%s-full-%dx%d.%s", timestamp, viewport.Width, viewport.Height, s.Config.FileFormat)
	filepath := filepath.Join(urlDir, filename)

	// Create base actions list
	var tasks []chromedp.Action

	// First navigate to the URL
	tasks = append(tasks, chromedp.Navigate(urlConfig.URL))

	// Log cookies before setting our custom ones
	tasks = append(tasks, SaveCookiesToFile(ctx, urlConfig, "before", urlDir, viewport, "full page"))

	// Set cookies if specified
	if len(urlConfig.Cookies) > 0 || len(urlConfig.LocalStorage) > 0 {
		// Add cookies if specified
		if len(urlConfig.Cookies) > 0 {
			log.Printf("Setting %d cookies for %s", len(urlConfig.Cookies), urlConfig.Name)
			tasks = append(tasks, chromedp.ActionFunc(func(ctx context.Context) error {
				// Create cookie expiration (180 days)
				expr := cdp.TimeSinceEpoch(time.Now().Add(180 * 24 * time.Hour))

				for _, cookie := range urlConfig.Cookies {
					// Extract domain from URL if not specified in cookie
					domain := cookie.Domain
					if domain == "" {
						// Use the URL's domain
						domain = extractDomainFromURL(urlConfig.URL)
					}

					// Set cookie path to root if not specified
					path := cookie.Path
					if path == "" {
						path = "/"
					}

					err := network.SetCookie(cookie.Name, cookie.Value).
						WithExpires(&expr).
						WithDomain(domain).
						WithPath(path).
						WithHTTPOnly(cookie.HTTPOnly).
						WithSecure(cookie.Secure).
						Do(ctx)

					if err != nil {
						return err
					}
				}
				return nil
			}))
		}

		// Set localStorage values if specified
		if len(urlConfig.LocalStorage) > 0 {
			log.Printf("Setting %d localStorage items for %s", len(urlConfig.LocalStorage), urlConfig.Name)
			for _, storage := range urlConfig.LocalStorage {
				jsScript := fmt.Sprintf(`localStorage.setItem("%s", "%s")`,
					escapeJSString(storage.Key), escapeJSString(storage.Value))
				tasks = append(tasks, chromedp.Evaluate(jsScript, nil))
			}
		}

		// Reload the page to ensure cookies and localStorage take effect
		tasks = append(tasks, chromedp.Reload())

		// Log cookies after setting our custom ones
		tasks = append(tasks, SaveCookiesToFile(ctx, urlConfig, "after", urlDir, viewport, "full page"))
	}

	// Add remaining actions for screenshot
	tasks = append(tasks,
		chromedp.Sleep(time.Duration(urlConfig.Delay)*time.Millisecond),

		// Scroll to bottom to load lazy content
		chromedp.Evaluate(`window.scrollTo(0, document.body.scrollHeight)`, nil),
		chromedp.Sleep(1*time.Second), // Wait for content to load

		// Scroll back to top
		chromedp.Evaluate(`window.scrollTo(0, 0)`, nil),
		chromedp.Sleep(1*time.Second), // Wait for animations to complete

		// Add additional delay to ensure all elements are loaded
		chromedp.Sleep(2*time.Second),
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
	)

	// Execute tasks
	if err := chromedp.Run(ctx, tasks...); err != nil {
		return err
	}

	// Save screenshot to file
	if err := os.WriteFile(filepath, buf, 0644); err != nil {
		return err
	}

	log.Printf("Captured full page screenshot for %s at viewport %dx%d: %s", urlConfig.Name, viewport.Width, viewport.Height, filepath)
	return nil
}

// captureViewportScreenshots captures screenshots divided by viewport
func (s *Screenshoter) captureViewportScreenshots(ctx context.Context, urlConfig config.URLConfig, viewport config.Viewport, urlDir string) error {
	var pageHeight float64
	timestamp := time.Now().Format("20060102-150405")

	// Create base actions list
	var tasks []chromedp.Action

	// First navigate to the URL
	tasks = append(tasks, chromedp.Navigate(urlConfig.URL))

	// Log cookies before setting our custom ones
	tasks = append(tasks, SaveCookiesToFile(ctx, urlConfig, "before-viewport", urlDir, viewport, "viewport"))

	// Set cookies if specified
	if len(urlConfig.Cookies) > 0 || len(urlConfig.LocalStorage) > 0 {
		// Add cookies if specified
		if len(urlConfig.Cookies) > 0 {
			log.Printf("Setting %d cookies for %s", len(urlConfig.Cookies), urlConfig.Name)
			tasks = append(tasks, chromedp.ActionFunc(func(ctx context.Context) error {
				// Create cookie expiration (180 days)
				expr := cdp.TimeSinceEpoch(time.Now().Add(180 * 24 * time.Hour))

				for _, cookie := range urlConfig.Cookies {
					// Extract domain from URL if not specified in cookie
					domain := cookie.Domain
					if domain == "" {
						// Use the URL's domain
						domain = extractDomainFromURL(urlConfig.URL)
					}

					// Set cookie path to root if not specified
					path := cookie.Path
					if path == "" {
						path = "/"
					}

					err := network.SetCookie(cookie.Name, cookie.Value).
						WithExpires(&expr).
						WithDomain(domain).
						WithPath(path).
						WithHTTPOnly(cookie.HTTPOnly).
						WithSecure(cookie.Secure).
						Do(ctx)

					if err != nil {
						return err
					}
				}
				return nil
			}))
		}

		// Set localStorage values if specified
		if len(urlConfig.LocalStorage) > 0 {
			log.Printf("Setting %d localStorage items for %s", len(urlConfig.LocalStorage), urlConfig.Name)
			for _, storage := range urlConfig.LocalStorage {
				jsScript := fmt.Sprintf(`localStorage.setItem("%s", "%s")`,
					escapeJSString(storage.Key), escapeJSString(storage.Value))
				tasks = append(tasks, chromedp.Evaluate(jsScript, nil))
			}
		}

		// Reload the page to ensure cookies and localStorage take effect
		tasks = append(tasks, chromedp.Reload())

		// Log cookies after setting our custom ones
		tasks = append(tasks, SaveCookiesToFile(ctx, urlConfig, "after-viewport", urlDir, viewport, "viewport"))
	}

	// Add remaining actions for screenshot
	tasks = append(tasks,
		chromedp.Sleep(time.Duration(urlConfig.Delay)*time.Millisecond),

		// Scroll to bottom to load lazy content
		chromedp.Evaluate(`window.scrollTo(0, document.body.scrollHeight)`, nil),
		chromedp.Sleep(1*time.Second), // Wait for content to load

		// Scroll back to top
		chromedp.Evaluate(`window.scrollTo(0, 0)`, nil),
		chromedp.Sleep(1*time.Second), // Wait for animations to complete

		chromedp.Evaluate(`Math.max(document.body.scrollHeight, document.documentElement.scrollHeight)`, &pageHeight),
	)

	// Execute tasks to get page height
	if err := chromedp.Run(ctx, chromedp.Tasks(tasks)); err != nil {
		return err
	}

	// Calculate how many viewport sections we need
	viewportHeight := float64(viewport.Height)
	viewportCount := int(pageHeight / viewportHeight)
	if float64(viewportCount)*viewportHeight < pageHeight {
		viewportCount++
	}

	// Capture each viewport section
	for i := 0; i < viewportCount; i++ {
		var buf []byte
		scrollPos := float64(i) * viewportHeight
		filename := fmt.Sprintf("%s-viewport-%dx%d-%d.%s", timestamp, viewport.Width, viewport.Height, i+1, s.Config.FileFormat)
		filepath := filepath.Join(urlDir, filename)

		// Scroll to position and capture screenshot of only the viewport
		if err := chromedp.Run(ctx,
			// Scroll to position
			chromedp.Evaluate(fmt.Sprintf(`window.scrollTo(0, %f)`, scrollPos), nil),
			chromedp.Sleep(500*time.Millisecond), // Give time for any animations to complete

			// Ensure device metrics are set to capture only viewport
			emulation.SetDeviceMetricsOverride(int64(viewport.Width), int64(viewport.Height), 1, false).
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

// extractDomainFromURL extracts a domain name from a URL for cookie setting
func extractDomainFromURL(url string) string {
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

// escapeJSString escapes a string for use in JavaScript
func escapeJSString(s string) string {
	// Replace backslash with double backslash
	s = strings.ReplaceAll(s, "\\", "\\\\")
	// Replace double quote with escaped double quote
	s = strings.ReplaceAll(s, "\"", "\\\"")
	// Replace single quote with escaped single quote
	s = strings.ReplaceAll(s, "'", "\\'")
	// Replace newline with escaped newline
	s = strings.ReplaceAll(s, "\n", "\\n")
	// Replace carriage return with escaped carriage return
	s = strings.ReplaceAll(s, "\r", "\\r")
	// Replace tab with escaped tab
	s = strings.ReplaceAll(s, "\t", "\\t")
	return s
}
