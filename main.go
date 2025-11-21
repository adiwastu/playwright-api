package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/playwright-community/playwright-go"
)

// 1. GLOBAL VARIABLE: We store the browser here so it can be reused
var browser playwright.Browser

type DownloadRequest struct {
	URL string `json:"url"`
}

type DownloadResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	File    string `json:"file,omitempty"`
	Error   string `json:"error,omitempty"`
}

func main() {
	// --- START IMPROVEMENT #1 (Initialize ONCE) ---
	log.Println("1. Starting Playwright Core...")
	pw, err := playwright.Run()
	if err != nil {
		log.Fatalf("❌ Failed to start Playwright: %v", err)
	}
	// Note: In a simple script, we rely on OS cleanup when main exits,
	// but normally you'd handle shutdown here.

	log.Println("2. Launching Global Browser Instance...")
	browser, err = pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(false),
		Args: []string{
			"--disable-blink-features=AutomationControlled",
			"--no-sandbox",
			"--disable-dev-shm-usage",
			"--start-maximized",
		},
	})
	if err != nil {
		log.Fatalf("❌ Failed to launch browser: %v", err)
	}
	// --- END IMPROVEMENT #1 ---

	http.HandleFunc("/download", downloadHandler)
	http.HandleFunc("/health", healthHandler)

	port := getEnv("PORT", "8080")
	log.Printf("🚀 Starting download API server on port %s", port)

	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("❌ Failed to start server: %v", err)
	}
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	jsonResponse(w, DownloadResponse{Success: true, Message: "Download API is running"})
}

func downloadHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req DownloadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errorResponse(w, "Invalid JSON payload", http.StatusBadRequest)
		return
	}

	if req.URL == "" {
		errorResponse(w, "URL is required", http.StatusBadRequest)
		return
	}

	log.Printf("📥 Received download request for: %s", req.URL)

	// Still using simple goroutine (No concurrency limits)
	go processDownload(req.URL)

	jsonResponse(w, DownloadResponse{
		Success: true,
		Message: "Download started in background",
	})
}

func processDownload(targetURL string) {
	log.Printf("🚀 Starting download process for: %s", targetURL)

	if err := runDownload(targetURL); err != nil {
		log.Printf("❌ Download failed for %s: %v", targetURL, err)
		return
	}

	log.Printf("✅ Download completed for: %s", targetURL)
}

func runDownload(targetURL string) error {
	// --- MODIFIED: We skip Launching Browser here. We use the global 'browser' var ---

	log.Println("3. Creating browser context (Incognito Tab)...")
	// We create a new Context for every request to keep cookies/session isolated
	context, err := browser.NewContext(playwright.BrowserNewContextOptions{
		UserAgent: playwright.String("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"),
		Viewport: &playwright.Size{
			Width:  1920,
			Height: 1080,
		},
		Locale:     playwright.String("en-US"),
		TimezoneId: playwright.String("America/New_York"),
		ExtraHttpHeaders: map[string]string{
			"Accept-Language": "en-US,en;q=0.9",
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create browser context: %v", err)
	}
	defer context.Close() // Close the tab when done

	log.Println("4. Creating new page...")
	page, err := context.NewPage()
	if err != nil {
		return fmt.Errorf("failed to create new page: %v", err)
	}

	log.Println("5. Injecting stealth scripts...")
	if err := page.AddInitScript(playwright.Script{
		Content: playwright.String(`
            Object.defineProperty(navigator, 'webdriver', { get: () => undefined });
            Object.defineProperty(navigator, 'plugins', { get: () => [1, 2, 3, 4, 5] });
            Object.defineProperty(navigator, 'languages', { get: () => ['en-US', 'en'] });
        `),
	}); err != nil {
		return fmt.Errorf("failed to inject stealth scripts: %v", err)
	}

	const downloadButtonSelector = `button:has-text("Download")`

	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = "."
	}
	downloadPath := filepath.Join(homeDir, "freepik_downloads")

	if err := os.MkdirAll(downloadPath, os.ModePerm); err != nil {
		return fmt.Errorf("failed to create download directory: %v", err)
	}

	log.Printf("7. Navigating to %s...", targetURL)
	// Kept your original timeout logic
	if _, err := page.Goto(targetURL, playwright.PageGotoOptions{
		Timeout:   playwright.Float(60000),
		WaitUntil: playwright.WaitUntilStateNetworkidle,
	}); err != nil {
		return fmt.Errorf("failed to navigate to page: %v", err)
	}

	// Kept your original static sleep
	time.Sleep(3 * time.Second)

	title, err := page.Title()
	if err != nil {
		return fmt.Errorf("failed to get page title: %v", err)
	}
	if title == "Access Denied" || title == "Just a moment..." {
		return fmt.Errorf("page blocked by anti-bot protection")
	}

	log.Println("8. Looking for download button...")
	if visible, _ := page.IsVisible(downloadButtonSelector); !visible {
		return fmt.Errorf("download button not found")
	}

	log.Println("9. Clicking the 'Download' button...")
	download, err := page.ExpectDownload(func() error {
		return page.Click(downloadButtonSelector, playwright.PageClickOptions{
			Timeout: playwright.Float(30000),
		})
	})
	if err != nil {
		return fmt.Errorf("failed to click download button: %v", err)
	}

	// Kept your original simple file saving
	filename := download.SuggestedFilename()
	saveFileTo := filepath.Join(downloadPath, filename)

	if err := download.SaveAs(saveFileTo); err != nil {
		return fmt.Errorf("failed to save file: %v", err)
	}

	return nil
}

func jsonResponse(w http.ResponseWriter, response DownloadResponse) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func errorResponse(w http.ResponseWriter, message string, statusCode int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(DownloadResponse{Success: false, Error: message})
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
