package screenshot

import (
	"context"
	"fmt"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
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
		"--shm-size=2g",                    // Increase shared memory size to 2GB
		"--memory=4g",                      // Limit container memory to 4GB
		"chromedp/headless-shell:latest",   // Use chromedp's official headless shell image
		"--disable-web-security",           // Disable web security for testing
		"--ignore-certificate-errors",      // Ignore SSL certificate errors
		"--allow-running-insecure-content", // Allow loading insecure content
		"--disable-dev-shm-usage",          // Don't use /dev/shm (prevents crashes)
		"--no-sandbox")                     // No sandbox for container environment

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

// setCookiesAndLocalStorage sets cookies and localStorage items for a URL and refreshes the page
func (s *Screenshoter) setCookiesAndLocalStorage(ctx context.Context, urlConfig config.URLConfig, viewport config.Viewport, urlDir, stage string, screenshotType string) chromedp.ActionFunc {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		// Check if these are defaultCookies (applied from config)
		defaultCookiesApplied := false

		// The issue is that we're not correctly detecting if the cookies come from DefaultCookies
		// Instead of checking for stage, we should look for a marker that can tell us
		// Let's add a special log message and check cookie names against defaultCookies

		// Flag to track if any cookie or localStorage values were changed
		needsRefresh := false

		// Add cookies if specified
		if len(urlConfig.Cookies) > 0 {
			// Check if these cookies match the DefaultCookies from the config
			// This is a better way to detect if we're applying DefaultCookies
			for _, cookie := range urlConfig.Cookies {
				for _, defaultCookie := range s.Config.DefaultCookies {
					if cookie.Name == defaultCookie.Name {
						defaultCookiesApplied = true
						log.Printf("Detected DefaultCookie being applied: %s", cookie.Name)
						break
					}
				}
				if defaultCookiesApplied {
					break
				}
			}

			log.Printf("Setting %d cookies for %s (using DefaultCookies: %v)",
				len(urlConfig.Cookies), urlConfig.Name, defaultCookiesApplied)

			// Get existing cookies first
			existingCookies, err := storage.GetCookies().Do(ctx)
			if err != nil {
				log.Printf("ERROR: Failed to get existing cookies: %v", err)
				return err
			}

			// Create a map of existing cookies for quick lookup
			existingCookieMap := make(map[string]string)
			for _, cookie := range existingCookies {
				key := cookie.Name + cookie.Path + cookie.Domain
				existingCookieMap[key] = cookie.Value
			}

			// Create cookie expiration (180 days)
			expr := cdp.TimeSinceEpoch(time.Now().Add(180 * 24 * time.Hour))

			// Flag to track if any cookie was actually set
			cookiesChanged := false

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

				// Check if this cookie already exists with the same value
				key := cookie.Name + path + domain
				if value, exists := existingCookieMap[key]; exists && value == cookie.Value {
					log.Printf("Cookie %s already exists with the same value, skipping", cookie.Name)
					continue
				}

				err := network.SetCookie(cookie.Name, cookie.Value).
					WithExpires(&expr).
					WithDomain(domain).
					WithPath(path).
					WithHTTPOnly(cookie.HTTPOnly).
					WithSecure(cookie.Secure).
					Do(ctx)

				if err != nil {
					log.Printf("ERROR: Failed to set cookie %s: %v", cookie.Name, err)
					return err
				}

				log.Printf("Successfully set cookie: %s=%s", cookie.Name, cookie.Value)
				cookiesChanged = true
			}

			if cookiesChanged {
				needsRefresh = true
			}
		}

		// Set localStorage values if specified
		if len(urlConfig.LocalStorage) > 0 {
			log.Printf("Setting %d localStorage items for %s", len(urlConfig.LocalStorage), urlConfig.Name)
			storageChanged := false

			for _, storage := range urlConfig.LocalStorage {
				jsScript := fmt.Sprintf(`
				(function() {
					const existingValue = localStorage.getItem("%s");
					if (existingValue === "%s") {
						console.log("localStorage key %s already has the same value, skipping");
						return false;
					}
					localStorage.setItem("%s", "%s");
					return true;
				})()`,
					escapeJSString(storage.Key),
					escapeJSString(storage.Value),
					escapeJSString(storage.Key),
					escapeJSString(storage.Key),
					escapeJSString(storage.Value))

				var changed bool
				if err := chromedp.Evaluate(jsScript, &changed).Do(ctx); err != nil {
					log.Printf("ERROR: Failed to set localStorage %s: %v", storage.Key, err)
					return err
				}

				if changed {
					log.Printf("Successfully set localStorage: %s=%s", storage.Key, storage.Value)
					storageChanged = true
				}
			}

			if storageChanged {
				needsRefresh = true
			}
		}

		// Only refresh if needed
		if needsRefresh || defaultCookiesApplied {
			log.Printf("Refreshing page to ensure cookies and localStorage are applied")
			if err := chromedp.Reload().Do(ctx); err != nil {
				return err
			}

			// Extra refresh for DefaultCookies to ensure they're fully applied
			if defaultCookiesApplied {
				log.Printf("Adding extra refresh to ensure DefaultCookies are fully applied")
				// Wait a bit more for DefaultCookies
				if err := chromedp.Sleep(500 * time.Millisecond).Do(ctx); err != nil {
					return err
				}

				// Verify cookies were actually set before continuing
				cookies, err := storage.GetCookies().Do(ctx)
				if err != nil {
					log.Printf("ERROR: Failed to get cookies after setting DefaultCookies: %v", err)
				} else {
					log.Printf("After setting DefaultCookies, found %d cookies:", len(cookies))
					for _, c := range cookies {
						log.Printf("  Cookie: %s=%s (domain: %s, path: %s)",
							c.Name, c.Value, c.Domain, c.Path)
					}
				}

				if err := chromedp.Reload().Do(ctx); err != nil {
					return err
				}
			}

			// Wait time for page to load after refresh
			if err := chromedp.Sleep(500 * time.Millisecond).Do(ctx); err != nil {
				return err
			}
		}

		// Log cookies after setting our custom ones
		return SaveCookiesToFile(ctx, urlConfig, stage, urlDir, viewport, screenshotType).Do(ctx)
	})
}

// CaptureURL captures screenshots for a given URL with all configured viewports
func (s *Screenshoter) CaptureURL(ctx context.Context, urlConfig config.URLConfig) error {
	viewportsCount := len(urlConfig.Viewports)
	timeoutDuration := 120*time.Second + time.Duration(60*viewportsCount)*time.Second
	ctx, cancel := context.WithTimeout(ctx, timeoutDuration)
	defer cancel()

	log.Printf("Set timeout of %v for URL %s with %d viewports", timeoutDuration, urlConfig.Name, viewportsCount)

	timestamp := time.Now().Format("20060102-150405")
	uniqueDirName := fmt.Sprintf("%s_%s", sanitizeFilename(urlConfig.Name), timestamp)

	urlDir := filepath.Join(s.Config.OutputDir, uniqueDirName)
	if err := os.MkdirAll(urlDir, 0755); err != nil {
		return fmt.Errorf("failed to create directory for URL %s: %w", urlConfig.Name, err)
	}

	log.Printf("Created unique directory for %s: %s", urlConfig.Name, uniqueDirName)

	viewproofNeeded := len(s.Config.ViewProof) > 0

	var wg sync.WaitGroup
	errChan := make(chan error, len(urlConfig.Viewports))
	viewportSem := make(chan struct{}, 3) // Process up to 3 viewports in parallel

	for i, viewport := range urlConfig.Viewports {
		wg.Add(1)
		go func(i int, viewport config.Viewport) {
			defer wg.Done()

			viewportSem <- struct{}{}
			defer func() { <-viewportSem }()

			viewportDirName := fmt.Sprintf("%dx%d", viewport.Width, viewport.Height)
			viewportDir := filepath.Join(urlDir, viewportDirName)
			if err := os.MkdirAll(viewportDir, 0755); err != nil {
				errChan <- fmt.Errorf("failed to create directory for viewport %s: %w", viewportDirName, err)
				return
			}

			log.Printf("Capturing screenshots for %s at viewport %dx%d", urlConfig.Name, viewport.Width, viewport.Height)

			// Apply ViewProof to all viewports by removing the "i == 0" condition
			if err := s.captureWithViewport(ctx, urlConfig, viewport, viewportDir, true, viewproofNeeded); err != nil {
				errChan <- fmt.Errorf("failed to capture screenshots for %s at viewport %dx%d: %w",
					urlConfig.Name, viewport.Width, viewport.Height, err)
				return
			}
		}(i, viewport)
	}

	wg.Wait()

	select {
	case err := <-errChan:
		return err
	default:
		return nil
	}
}

// captureWithViewport captures screenshots for a specific viewport size
func (s *Screenshoter) captureWithViewport(ctx context.Context, urlConfig config.URLConfig, viewport config.Viewport, viewportDir string, captureViewports bool, withViewProof bool) error {
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

	// If withViewProof is true, capture a full page screenshot with ViewProof first
	if withViewProof {
		if err := s.captureFullPageWithViewProof(browserCtx, urlConfig, viewport, viewportDir); err != nil {
			return fmt.Errorf("failed to capture full-proof screenshot: %w", err)
		}
	}

	// Capture full page screenshot
	if err := s.captureFullPageScreenshot(browserCtx, urlConfig, viewport, viewportDir); err != nil {
		return fmt.Errorf("failed to capture full page screenshot for %s at viewport %dx%d: %w",
			urlConfig.Name, viewport.Width, viewport.Height, err)
	}

	// Capture viewport screenshots if requested
	if captureViewports {
		if err := s.captureViewportScreenshots(browserCtx, urlConfig, viewport, viewportDir, true); err != nil {
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
	// Use the URL name directly from the config
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
	filename := fmt.Sprintf("%s-cookies.csv", sanitizeFilename(urlConfig.Name))
	filepath := filepath.Join(urlDir, filename)

	log.Printf("Saving cookies to CSV file: %s", filepath)

	writeHeader := true
	if _, err := os.Stat(filepath); err == nil {
		writeHeader = false
		log.Printf("CSV file exists, appending without headers")
	} else {
		log.Printf("CSV file does not exist, will create with headers")
	}

	file, err := os.OpenFile(filepath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("ERROR: Failed to open CSV file: %v", err)
		return err
	}
	defer file.Close()

	if writeHeader {
		headerLine := "Timestamp,URL,URL_Name,Stage,Screenshot_Type,Viewport,Cookie_Name,Cookie_Value,Domain,Path,Expires,Size,HttpOnly,Secure,Session,SameSite,Priority\n"
		if _, err := file.WriteString(headerLine); err != nil {
			log.Printf("ERROR: Failed to write CSV header: %v", err)
			return err
		}
		log.Printf("Wrote CSV headers")
	}

	log.Printf("Writing %d cookies to CSV", len(cookies))
	for _, cookie := range cookies {
		urlValue := strings.ReplaceAll(urlConfig.URL, ",", "\\,")
		urlName := strings.ReplaceAll(urlConfig.Name, ",", "\\,")
		cookieName := strings.ReplaceAll(cookie.Name, ",", "\\,")
		cookieValue := strings.ReplaceAll(cookie.Value, ",", "\\,")
		cookieDomain := strings.ReplaceAll(cookie.Domain, ",", "\\,")
		cookiePath := strings.ReplaceAll(cookie.Path, ",", "\\,")

		expiresStr := time.Unix(int64(cookie.Expires), 0).Format("2006-01-02 15:04:05")

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

// captureFullPageWithViewProof captures a special screenshot with ViewProof data
func (s *Screenshoter) captureFullPageWithViewProof(ctx context.Context, urlConfig config.URLConfig, viewport config.Viewport, viewportDir string) error {
	if len(s.Config.ViewProof) == 0 {
		return nil // Skip if ViewProof is not needed
	}

	log.Printf("Capturing special full-proof screenshot with ViewProof data")

	var buf []byte
	timestamp := time.Now().Format("20060102-150405")
	filename := fmt.Sprintf("%s-full-proof-%dx%d.%s", timestamp, viewport.Width, viewport.Height, s.Config.FileFormat)
	filepath := filepath.Join(viewportDir, filename)

	viewproofData := make(map[string]string)
	var tasks []chromedp.Action

	tasks = append(tasks, chromedp.Navigate(urlConfig.URL))
	tasks = append(tasks, SaveCookiesToFile(ctx, urlConfig, "before", viewportDir, viewport, "full-proof"))

	// Apply cookies and localStorage BEFORE extracting ViewProof data
	if len(urlConfig.Cookies) > 0 || len(urlConfig.LocalStorage) > 0 {
		tasks = append(tasks, s.setCookiesAndLocalStorage(ctx, urlConfig, viewport, viewportDir, "after", "full-proof"))

		// Add explicit refresh after setting cookies/localStorage to ensure they're applied
		tasks = append(tasks, chromedp.ActionFunc(func(ctx context.Context) error {
			log.Printf("Performing additional refresh to ensure cookies and localStorage are fully applied before ViewProof processing")
			if err := chromedp.Reload().Do(ctx); err != nil {
				return err
			}
			// Wait for page to reload and stabilize
			return chromedp.Sleep(1 * time.Second).Do(ctx)
		}))
	}

	// Extract ViewProof data from cookies and localStorage AFTER setting them
	tasks = append(tasks, chromedp.ActionFunc(func(ctx context.Context) error {
		cookies, err := storage.GetCookies().Do(ctx)
		if err != nil {
			log.Printf("ERROR: Failed to get cookies for viewproof: %v", err)
			return nil // Non-fatal error
		}

		for _, cookie := range cookies {
			for _, proofKey := range s.Config.ViewProof {
				if cookie.Name == proofKey {
					viewproofData[fmt.Sprintf("cookie:%s", cookie.Name)] = cookie.Value
				}
			}
		}

		for _, proofKey := range s.Config.ViewProof {
			var value string
			err := chromedp.Evaluate(fmt.Sprintf(`localStorage.getItem("%s")`, escapeJSString(proofKey)), &value).Do(ctx)
			if err == nil && value != "" {
				viewproofData[fmt.Sprintf("localStorage:%s", proofKey)] = value
			}
		}

		log.Printf("Extracted %d viewproof values for full-proof screenshot", len(viewproofData))
		return nil
	}))

	// Scroll to ensure lazy content is loaded
	tasks = append(tasks,
		chromedp.Sleep(time.Duration(urlConfig.Delay)*time.Millisecond),
		chromedp.Evaluate(`window.scrollTo(0, document.body.scrollHeight)`, nil),
		chromedp.Sleep(500*time.Millisecond),
		chromedp.Evaluate(`window.scrollTo(0, 0)`, nil),
		chromedp.Sleep(500*time.Millisecond),
	)

	// Add ViewProof block
	tasks = append(tasks, chromedp.ActionFunc(func(ctx context.Context) error {
		if len(viewproofData) > 0 {
			script, _ := s.createViewProof(viewproofData, true, false)

			var result bool
			err := chromedp.Evaluate(script, &result).Do(ctx)
			if err != nil {
				log.Printf("ERROR creating ViewProof block: %v", err)
				return err
			}

			log.Printf("Added ViewProof block to proof screenshot")
		}
		return nil
	}))

	tasks = append(tasks, chromedp.Sleep(1*time.Second))
	tasks = append(tasks, chromedp.Sleep(500*time.Millisecond))

	// Capture the screenshot
	tasks = append(tasks, chromedp.ActionFunc(func(ctx context.Context) error {
		var metrics map[string]interface{}
		if err := chromedp.Evaluate(`({
			width: Math.max(document.body.scrollWidth, document.documentElement.scrollWidth),
			height: Math.max(document.body.scrollHeight, document.documentElement.scrollHeight),
		})`, &metrics).Do(ctx); err != nil {
			return err
		}

		width := int64(viewport.Width)

		// Limit height to prevent Chrome screenshot issues
		height := int64(metrics["height"].(float64))
		maxHeight := int64(16384)
		if height > maxHeight {
			log.Printf("Warning: Page height (%d) exceeds maximum allowed height (%d). Limiting height.",
				height, maxHeight)
			height = maxHeight
		}

		if err := emulation.SetDeviceMetricsOverride(width, height, 1, false).Do(ctx); err != nil {
			return err
		}

		err := chromedp.CaptureScreenshot(&buf).Do(ctx)
		if err != nil {
			// Try with smaller height if capture failed
			if height > 8192 {
				log.Printf("Screenshot capture failed, trying with reduced height...")
				if err := emulation.SetDeviceMetricsOverride(width, 8192, 1, false).Do(ctx); err != nil {
					return err
				}
				return chromedp.CaptureScreenshot(&buf).Do(ctx)
			}
			return err
		}

		return nil
	}))

	if err := chromedp.Run(ctx, tasks...); err != nil {
		return err
	}

	if err := os.WriteFile(filepath, buf, 0644); err != nil {
		return err
	}

	log.Printf("Captured full-proof screenshot for %s at viewport %dx%d: %s", urlConfig.Name, viewport.Width, viewport.Height, filepath)
	return nil
}

// captureFullPageScreenshot captures a full page screenshot
func (s *Screenshoter) captureFullPageScreenshot(ctx context.Context, urlConfig config.URLConfig, viewport config.Viewport, viewportDir string) error {
	var buf []byte
	timestamp := time.Now().Format("20060102-150405")
	filename := fmt.Sprintf("%s-full-%dx%d.%s", timestamp, viewport.Width, viewport.Height, s.Config.FileFormat)
	filepath := filepath.Join(viewportDir, filename)

	var tasks []chromedp.Action

	tasks = append(tasks, chromedp.Navigate(urlConfig.URL))
	tasks = append(tasks, SaveCookiesToFile(ctx, urlConfig, "before", viewportDir, viewport, "full page"))

	// First apply cookies and localStorage
	if len(urlConfig.Cookies) > 0 || len(urlConfig.LocalStorage) > 0 {
		tasks = append(tasks, s.setCookiesAndLocalStorage(ctx, urlConfig, viewport, viewportDir, "after", "full page"))

		// Add explicit refresh after setting cookies/localStorage to ensure they're applied
		tasks = append(tasks, chromedp.ActionFunc(func(ctx context.Context) error {
			log.Printf("Performing additional refresh to ensure cookies and localStorage are fully applied before screenshot capture")
			if err := chromedp.Reload().Do(ctx); err != nil {
				return err
			}
			// Wait for page to reload and stabilize
			return chromedp.Sleep(1 * time.Second).Do(ctx)
		}))
	}

	// Then extract ViewProof data if needed
	var viewproofData map[string]string
	if len(s.Config.ViewProof) > 0 {
		viewproofData = make(map[string]string)

		tasks = append(tasks, chromedp.ActionFunc(func(ctx context.Context) error {
			cookies, err := storage.GetCookies().Do(ctx)
			if err != nil {
				log.Printf("ERROR: Failed to get cookies for viewproof: %v", err)
				return nil // Non-fatal error
			}

			for _, cookie := range cookies {
				for _, proofKey := range s.Config.ViewProof {
					if cookie.Name == proofKey {
						viewproofData[fmt.Sprintf("cookie:%s", cookie.Name)] = cookie.Value
					}
				}
			}

			for _, proofKey := range s.Config.ViewProof {
				var value string
				err := chromedp.Evaluate(fmt.Sprintf(`localStorage.getItem("%s")`, escapeJSString(proofKey)), &value).Do(ctx)
				if err == nil && value != "" {
					viewproofData[fmt.Sprintf("localStorage:%s", proofKey)] = value
				}
			}

			log.Printf("Extracted %d viewproof values", len(viewproofData))
			return nil
		}))
	}

	tasks = append(tasks,
		chromedp.Sleep(time.Duration(urlConfig.Delay)*time.Millisecond),
		chromedp.Evaluate(`window.scrollTo(0, document.body.scrollHeight)`, nil),
		chromedp.Sleep(500*time.Millisecond),
		chromedp.Evaluate(`window.scrollTo(0, 0)`, nil),
		chromedp.Sleep(500*time.Millisecond),
	)

	tasks = append(tasks, chromedp.Sleep(1*time.Second))

	tasks = append(tasks, chromedp.ActionFunc(func(ctx context.Context) error {
		var metrics map[string]interface{}
		if err := chromedp.Evaluate(`({
			width: Math.max(document.body.scrollWidth, document.documentElement.scrollWidth),
			height: Math.max(document.body.scrollHeight, document.documentElement.scrollHeight),
		})`, &metrics).Do(ctx); err != nil {
			return err
		}

		width := int64(viewport.Width)

		height := int64(metrics["height"].(float64))
		maxHeight := int64(16384)
		if height > maxHeight {
			log.Printf("Warning: Page height (%d) exceeds maximum allowed height (%d). Limiting height.",
				height, maxHeight)
			height = maxHeight
		}

		if err := emulation.SetDeviceMetricsOverride(width, height, 1, false).Do(ctx); err != nil {
			return err
		}

		err := chromedp.CaptureScreenshot(&buf).Do(ctx)
		if err != nil {
			if height > 8192 {
				log.Printf("Screenshot capture failed, trying with reduced height...")
				if err := emulation.SetDeviceMetricsOverride(width, 8192, 1, false).Do(ctx); err != nil {
					return err
				}
				return chromedp.CaptureScreenshot(&buf).Do(ctx)
			}
			return err
		}

		if len(s.Config.ViewProof) > 0 && len(viewproofData) > 0 {
			overlayText := fmt.Sprintf("VIEWPROOF DATA - %s", timestamp)
			for key, value := range viewproofData {
				overlayText += fmt.Sprintf("\n%s: %s", key, value)
			}

			log.Printf("Adding ViewProof data as direct text overlay on image")
			log.Printf("ViewProof data: %s", overlayText)
		}

		return nil
	}))

	if err := chromedp.Run(ctx, tasks...); err != nil {
		return err
	}

	if err := os.WriteFile(filepath, buf, 0644); err != nil {
		return err
	}

	log.Printf("Captured full page screenshot for %s at viewport %dx%d: %s", urlConfig.Name, viewport.Width, viewport.Height, filepath)
	return nil
}

// captureViewportScreenshots captures screenshots divided by viewport
func (s *Screenshoter) captureViewportScreenshots(ctx context.Context, urlConfig config.URLConfig, viewport config.Viewport, viewportDir string, captureViewports bool) error {
	var pageHeight float64
	timestamp := time.Now().Format("20060102-150405")

	var tasks []chromedp.Action

	tasks = append(tasks, chromedp.Navigate(urlConfig.URL))
	tasks = append(tasks, SaveCookiesToFile(ctx, urlConfig, "before-viewport", viewportDir, viewport, "viewport"))

	if len(urlConfig.Cookies) > 0 || len(urlConfig.LocalStorage) > 0 {
		tasks = append(tasks, s.setCookiesAndLocalStorage(ctx, urlConfig, viewport, viewportDir, "after-viewport", "viewport"))

		// Add explicit refresh after setting cookies/localStorage to ensure they're applied
		tasks = append(tasks, chromedp.ActionFunc(func(ctx context.Context) error {
			log.Printf("Performing additional refresh to ensure cookies and localStorage are fully applied before viewport screenshots")
			if err := chromedp.Reload().Do(ctx); err != nil {
				return err
			}
			// Wait for page to reload and stabilize
			return chromedp.Sleep(1 * time.Second).Do(ctx)
		}))
	}

	tasks = append(tasks,
		chromedp.Sleep(time.Duration(urlConfig.Delay)*time.Millisecond),
		chromedp.Evaluate(`window.scrollTo(0, document.body.scrollHeight)`, nil),
		chromedp.Sleep(500*time.Millisecond),
		chromedp.Evaluate(`window.scrollTo(0, 0)`, nil),
		chromedp.Sleep(500*time.Millisecond),
	)

	tasks = append(tasks, chromedp.Evaluate(`Math.max(document.body.scrollHeight, document.documentElement.scrollHeight)`, &pageHeight))

	if err := chromedp.Run(ctx, chromedp.Tasks(tasks)); err != nil {
		return err
	}

	viewportHeight := float64(viewport.Height)

	if viewportHeight < 200 {
		log.Printf("Warning: Small viewport height detected (%f). This might cause overlap issues.", viewportHeight)
	}

	viewportCount := int(math.Ceil(pageHeight / viewportHeight))

	if viewportCount < 1 {
		viewportCount = 1
	}

	log.Printf("Page height: %f, Viewport height: %f, Will capture %d viewport screenshots",
		pageHeight, viewportHeight, viewportCount)

	if pageHeight <= viewportHeight || viewportCount == 1 {
		var buf []byte
		filename := fmt.Sprintf("%s-viewport-%dx%d-1.%s", timestamp, viewport.Width, viewport.Height, s.Config.FileFormat)
		filepath := filepath.Join(viewportDir, filename)

		if err := chromedp.Run(ctx,
			chromedp.Evaluate(`window.scrollTo(0, 0)`, nil),
			chromedp.Sleep(300*time.Millisecond),

			emulation.SetDeviceMetricsOverride(int64(viewport.Width), int64(viewport.Height), 1, false).
				WithScreenOrientation(&emulation.ScreenOrientation{
					Type:  emulation.OrientationTypePortraitPrimary,
					Angle: 0,
				}),

			chromedp.Sleep(800*time.Millisecond),
			chromedp.CaptureScreenshot(&buf),
		); err != nil {
			return err
		}

		if err := os.WriteFile(filepath, buf, 0644); err != nil {
			return err
		}

		log.Printf("Captured single viewport screenshot for %s: %s", urlConfig.Name, filepath)
		return nil
	}

	var wg sync.WaitGroup
	errChan := make(chan error, viewportCount)
	vpSem := make(chan struct{}, 4) // Process up to 4 viewport sections in parallel

	for i := 0; i < viewportCount; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()

			vpSem <- struct{}{}
			defer func() { <-vpSem }()

			scrollPos := float64(i) * viewportHeight

			if i == viewportCount-1 && scrollPos+viewportHeight > pageHeight {
				scrollPos = pageHeight - viewportHeight
				if scrollPos < 0 {
					scrollPos = 0
				}
			}

			filename := fmt.Sprintf("%s-viewport-%dx%d-%d.%s", timestamp, viewport.Width, viewport.Height, i+1, s.Config.FileFormat)
			filepath := filepath.Join(viewportDir, filename)

			var buf []byte
			if err := chromedp.Run(ctx,
				chromedp.Evaluate(fmt.Sprintf(`window.scrollTo({top: %f, left: 0, behavior: 'instant'})`, scrollPos), nil),
				chromedp.Sleep(300*time.Millisecond),

				emulation.SetDeviceMetricsOverride(int64(viewport.Width), int64(viewport.Height), 1, false).
					WithScreenOrientation(&emulation.ScreenOrientation{
						Type:  emulation.OrientationTypePortraitPrimary,
						Angle: 0,
					}),

				chromedp.Sleep(800*time.Millisecond),
				chromedp.CaptureScreenshot(&buf),
			); err != nil {
				errChan <- err
				return
			}

			if err := os.WriteFile(filepath, buf, 0644); err != nil {
				errChan <- err
				return
			}

			log.Printf("Captured viewport screenshot for %s: %s", urlConfig.Name, filepath)
		}(i)
	}

	wg.Wait()

	select {
	case err := <-errChan:
		return err
	default:
		return nil
	}
}

// extractDomainFromURL extracts a domain name from a URL for cookie setting
func extractDomainFromURL(url string) string {
	if strings.HasPrefix(url, "http://") {
		url = url[7:]
	} else if strings.HasPrefix(url, "https://") {
		url = url[8:]
	}

	if strings.HasPrefix(url, "www.") {
		url = url[4:]
	}

	if idx := strings.Index(url, "/"); idx > 0 {
		url = url[:idx]
	}

	if idx := strings.Index(url, ":"); idx > 0 {
		url = url[:idx]
	}

	return url
}

// formatViewproofData formats viewproof data for display in the ViewProof block
func formatViewproofData(data map[string]string) string {
	var formattedData strings.Builder
	for key, value := range data {
		formattedData.WriteString(fmt.Sprintf("%s: %s\n", key, value))
	}
	return formattedData.String()
}

// escapeJSString escapes a string for embedding in a JavaScript string
func escapeJSString(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "'", "\\'")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	s = strings.ReplaceAll(s, "\n", "\\n")
	s = strings.ReplaceAll(s, "\r", "\\r")
	s = strings.ReplaceAll(s, "\t", "\\t")
	return s
}

// createViewProof creates JavaScript code to inject a ViewProof overlay/block
func (s *Screenshoter) createViewProof(viewproofData map[string]string, forceful bool, separateCSS bool) (string, string) {
	formattedData := formatViewproofData(viewproofData)

	elementID := "viewproof-block"
	if forceful {
		elementID = "super-viewproof-overlay"
	}

	var script string
	if forceful {
		script = `
		(function() {
			try {
				var existing = document.getElementById('` + elementID + `');
				if (existing) {
					existing.remove();
				}
				
				var overlay = document.createElement('div');
				overlay.id = '` + elementID + `';
				
				const overlayStyles = {
					position: 'fixed',
					top: 0,
					left: 0, 
					opacity: 0.9,
					zIndex: '9999999',
					width: '100%',
					boxSizing: 'border-box',
					backgroundColor: 'blue',
					color: 'white',
					padding: '20px',
					border: '1px solid #FFFF00',
					boxShadow: '0 0 50px rgba(0,0,0,0.8)',
					fontSize: '30px',
					fontFamily: 'Arial, sans-serif',
					fontWeight: 'bold',
					lineHeight: '1.5',
					textAlign: 'center'
				};
				
				Object.assign(overlay.style, overlayStyles);
				
				var title = document.createElement('h2');
				title.innerText = '🤖 VIEWPROOF DATA';
				title.classList.add('viewproof-title');
				
				const titleStyles = {
					margin: '0 0 20px 0',
					fontSize: '40px',
					textDecoration: 'underline',
					textAlign: 'center',
					display: 'block',
					width: '100%'
				};
				Object.assign(title.style, titleStyles);
				
				overlay.appendChild(title);
				
				var pre = document.createElement('pre');
				
				const preStyles = {
					backgroundColor: 'black',
					color: '#00FF00',
					border: '5px solid white',
					boxSizing: 'border-box',
					padding: '15px',
					margin: '10px auto',
					fontSize: '20px',
					textAlign: 'left',
					maxWidth: '800px',
					overflow: 'visible',
					wordWrap: 'break-word'
				};
				Object.assign(pre.style, preStyles);
				
				pre.innerText = "` + escapeJSString(formattedData) + `";
				overlay.appendChild(pre);
				
				document.body.prepend(overlay);
				
				console.log('ViewProof overlay created successfully');
				return true;
			} catch(e) {
				console.error('Error creating ViewProof overlay:', e);
				return false;
			}
		})();
		`
	} else {
		script = `
		(function() {
			let block = document.getElementById('` + elementID + `');
			
			if (!block) {
				let style = document.createElement('style');
				style.textContent = '%s';
				document.head.appendChild(style);
				
				block = document.createElement('div');
				block.id = '` + elementID + `';
				block.innerHTML = '<div class="viewproof-title">🤖 VIEWPROOF DATA</div><pre class="viewproof-content">` + escapeJSString(formattedData) + `</pre>';
				document.body.appendChild(block);
				console.log('ViewProof block created and made visible');
				return true;
			}
			
			block.className = 'viewproof-important';
			document.body.appendChild(block);
			
			console.log('ViewProof block visibility enforced');
			return true;
		})();
		`
	}

	css := `/* ViewProof Block Styles */
	#` + elementID + ` {
		position: fixed !important;
		top: 0 !important;
		left: 0 !important;
		z-index: 2147483647 !important;
		width: 100% !important;
		display: block !important;
		visibility: visible !important;
		opacity: 1 !important;
		background-color:rgb(0, 55, 255) !important;
		color: white !important;
		padding: 20px !important;
		font-size: 24px !important;
		text-align: center !important;
	}

	.viewproof-important {
		position: fixed !important;
		top: 0 !important;
		left: 0 !important;
		width: 100% !important;
		background-color:rgb(255, 0, 251) !important;
		color: white !important;
		padding: 20px !important;
		font-size: 24px !important;
		z-index: 2147483647 !important;
		display: block !important;
		visibility: visible !important;
		opacity: 1 !important;
	}
	
	#` + elementID + ` pre {
		background-color: black !important;
		color: #00FF00 !important;
		border: 5px solid white !important;
		max-width: 800px !important;
		overflow: visible !important;
		word-wrap: break-word !important;
		padding: 15px !important;
		margin: 10px auto !important;
		font-size: 20px !important;
		text-align: left !important;
	}
	
	.viewproof-title {
		font-size: 22px !important;
		font-weight: bold !important;
		margin-bottom: 10px !important;
		text-align: center !important;
		width: 100% !important;
		display: block !important;
	}
	
	.viewproof-content {
		text-align: left !important;
		background: black !important;
		padding: 10px !important;
		margin: 0 !important;
	}`

	if !separateCSS {
		return script, ""
	}

	return script, css
}

// CaptureURLs captures screenshots for all URLs in configuration
func (s *Screenshoter) CaptureURLs(ctx context.Context) error {
	sem := make(chan struct{}, s.Config.Concurrency)
	errChan := make(chan error, len(s.Config.URLs))
	doneChan := make(chan struct{}, len(s.Config.URLs))

	for _, urlConfig := range s.Config.URLs {
		urlConfig := urlConfig // Create local copy for goroutine
		sem <- struct{}{}

		go func() {
			defer func() {
				<-sem
				doneChan <- struct{}{}
			}()

			if err := s.CaptureURL(ctx, urlConfig); err != nil {
				errChan <- fmt.Errorf("error capturing URL %s: %w", urlConfig.Name, err)
			}
		}()
	}

	for i := 0; i < len(s.Config.URLs); i++ {
		<-doneChan
	}

	select {
	case err := <-errChan:
		return err
	default:
		return nil
	}
}
