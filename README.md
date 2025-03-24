# Screenshot Tool

A robust Go application that automatically captures and analyzes screenshots of web pages.

## Features

- Captures full-page screenshots of entire web page content
- Generates viewport-limited screenshots divided into sections
- Supports concurrent processing of multiple URLs
- Customizable viewport dimensions
- Configurable page loading delay times
- Organized screenshot storage with consistent naming
- Cookie/localStorage management with automatic refresh after setting
- ViewProof overlay for validation of cookies and localStorage values
- CSV cookie logging for easy analysis
- Enhanced error diagnostics with better error messages
- SSL certificate error bypass for testing environments

## Requirements

- Go 1.18 or later
- One of the following:
  - Chrome/Chromium browser installed locally
  - Docker installed (for automatic Docker Chrome fallback)

### Chrome Selection Logic

The tool automatically selects Chrome in this priority order:

1. If Chrome is installed locally, use the local Chrome executable
2. If Docker is installed, automatically start a Chrome container
3. Fall back to default Chrome settings

You can override this automatic selection using the `-chrome` command-line flag:
```bash
go run main.go -chrome=local    # Force use of local Chrome executable
go run main.go -chrome=docker   # Force use of Docker Chrome container
go run main.go -chrome=auto     # Automatic selection (local, then Docker)
```

### Local Chrome Installation

The application will attempt to automatically locate Chrome in common installation locations:

- **macOS**: 
  - `/Applications/Google Chrome.app/Contents/MacOS/Google Chrome`
  - `/Applications/Google Chrome Canary.app/Contents/MacOS/Google Chrome Canary`
  - `/Applications/Chromium.app/Contents/MacOS/Chromium`

- **Windows**:
  - `C:\Program Files\Google\Chrome\Application\chrome.exe`
  - `C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`
  - `%LOCALAPPDATA%\Google\Chrome\Application\chrome.exe`

- **Linux**:
  - `/usr/bin/google-chrome`
  - `/usr/bin/chromium`
  - `/usr/bin/chromium-browser`
  - `/snap/bin/chromium`

If your Chrome installation is in a non-standard location, you can set the `CHROME_PATH` environment variable:

```bash
export CHROME_PATH=/path/to/your/chrome
```

### Docker Chrome

The tool uses ChromeDP's official `chromedp/headless-shell` image, which is specifically designed to work with the ChromeDP library. When you use the `-chrome=docker` flag, the tool will:

1. Check if a Chrome container is already running
2. Start a new Chrome container if needed with the appropriate settings
3. Verify that Chrome is responding before proceeding
4. Apply necessary configurations for screenshot capture
5. Clean up the container when finished (unless it was already running)

No manual Docker setup is needed - simply use:

```bash
go run main.go -chrome=docker -config=config-basic.json
```

## Installation

1. Clone the repository:
```bash
git clone https://github.com/yourusername/screenshot-tool.git
cd screenshot-tool
```

2. Install dependencies:
```bash
go mod tidy
```

## Usage

The tool provides two configuration files to get you started:

- **config-basic.json**: A simple configuration for basic usage
- **config-advanced.json**: A comprehensive configuration with advanced features like ViewProof

### Basic Configuration

For simple usage, start with the basic configuration:

```bash
go run main.go -config=config-basic.json
```

The basic configuration includes:
- A single URL with multiple viewport sizes
- Basic cookie and localStorage management
- Common screenshot settings

### Advanced Configuration

For more complex scenarios, use the advanced configuration:

```bash
go run main.go -config=config-advanced.json
```

The advanced configuration includes:
- Multiple URLs with different settings
- ViewProof functionality to validate cookies and localStorage
- More comprehensive cookie management
- Mobile viewport sizes

### Configuration Files

1. Example of `config-basic.json`:
```json
{
  "urls": [
    {
      "name": "example-site",
      "url": "https://example.com",
      "viewports": [
        {
          "width": 1920,
          "height": 1080
        },
        {
          "width": 768,
          "height": 1024
        }
      ],
      "delay": 1000,
      "cookies": [
        {
          "name": "session",
          "value": "test-session",
          "path": "/",
          "secure": false,
          "httpOnly": false
        }
      ]
    }
  ],
  "defaultViewports": [
    {
      "width": 1920,
      "height": 1080
    },
    {
      "width": 768,
      "height": 1024
    }
  ],
  "defaultCookies": [
    {
      "name": "region",
      "value": "us-east-1",
      "path": "/",
      "secure": false,
      "httpOnly": false
    }
  ],
  "outputDir": "./screenshots",
  "fileFormat": "png",
  "quality": 90,
  "concurrency": 4
}
```

2. Build the tool:
```bash
go build -o screenshot-tool
```

## Configuration Options

| Option | Description |
|--------|-------------|
| `urls` | Array of URL objects to process |
| `defaultViewports` | Array of default viewport dimensions |
| `defaultCookies` | Default cookies to set for all URLs |
| `viewproof` | List of cookie/localStorage keys to extract and display in screenshots |
| `outputDir` | Directory to save screenshots |
| `fileFormat` | Image format (png or jpeg) |
| `quality` | Image quality (1-100) |
| `concurrency` | Number of URLs to process simultaneously |
| `chromeMode` | Chrome execution mode: "local", "docker", or "auto" |

### URL Object Options

| Option | Description |
|--------|-------------|
| `name` | Identifier for the URL (used in filenames) |
| `url` | URL to capture |
| `viewports` | Array of custom viewport dimensions (optional) |
| `delay` | Page load delay in milliseconds (optional) |
| `cookies` | Array of cookies to set before capturing (optional) |
| `localStorage` | Array of localStorage key-value pairs to set (optional) |

### Cookie Object Options

| Option | Description |
|--------|-------------|
| `name` | Cookie name |
| `value` | Cookie value |
| `domain` | Cookie domain (optional, defaults to URL domain) |
| `path` | Cookie path (optional, defaults to "/") |
| `secure` | Whether cookie is secure (optional) |
| `httpOnly` | Whether cookie is HTTP only (optional) |

## ViewProof Feature

The ViewProof feature allows you to overlay key cookie and localStorage values directly on screenshots, making it easy to validate that specific values are being applied correctly. To use this feature:

1. Set the `viewproof` array in your configuration with the keys you want to monitor
2. Run the tool as normal
3. The tool will generate full-proof screenshots with these values displayed

This is particularly useful for:
- Validating geo-region targeting
- Confirming A/B test assignments
- Verifying user preferences are being applied

Example ViewProof configuration:
```json
"viewproof": ["user_region", "gdpr-consent", "user_preferences"]
```

## Output Organization

Screenshots are saved in the following directory structure:

```
outputDir/
  └── urlName_timestamp/
      ├── viewportWidth×viewportHeight/
      │   ├── timestamp-full-widthxheight.png
      │   ├── timestamp-viewport-widthxheight-1.png
      │   ├── timestamp-viewport-widthxheight-2.png
      │   └── ...
      └── urlName-cookies.csv
```

Each viewport gets its own directory, containing:
- A full-page screenshot
- Individual viewport screenshots
- A ViewProof screenshot if configured

Cookie data is saved to a CSV file for easy analysis. 