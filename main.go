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
	http.HandleFunc("/download", downloadHandler)
	http.HandleFunc("/health", healthHandler)

	port := getEnv("PORT", "8080")
	log.Printf("🚀 Starting download API server on port %s", port)
	log.Printf("📝 Endpoint: POST http://localhost:%s/download", port)
	
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("❌ Failed to start server: %v", err)
	}
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	jsonResponse(w, DownloadResponse{
		Success: true,
		Message: "Download API is running",
	})
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

	// Process the download in a goroutine so we can return immediately
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
	log.Println("1. Starting Playwright...")
	pw, err := playwright.Run()
	if err != nil {
		return fmt.Errorf("failed to start Playwright: %v", err)
	}
	defer pw.Stop()
	log.Println("✅ Playwright started")

	time.Sleep(2 * time.Second)

	log.Println("2. Launching browser...")
	browser, err := pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(false),
		Args: []string{
			"--disable-blink-features=AutomationControlled",
			"--no-sandbox",
			"--disable-dev-shm-usage",
			"--start-maximized",
		},
	})
	if err != nil {
		return fmt.Errorf("failed to launch browser: %v", err)
	}
	defer browser.Close()
	log.Println("✅ Browser launched")

	log.Println("3. Creating browser context...")
	context, err := browser.NewContext(playwright.BrowserNewContextOptions{
		UserAgent: playwright.String("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"),
		Viewport: &playwright.Size{
			Width:  1920,
			Height: 1080,
		},
		Locale:           playwright.String("en-US"),
		TimezoneId:       playwright.String("America/New_York"),
		Permissions:      []string{"geolocation"},
		ExtraHttpHeaders: map[string]string{
			"Accept-Language": "en-US,en;q=0.9",
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create browser context: %v", err)
	}
	defer context.Close()
	log.Println("✅ Browser context created")

	log.Println("4. Creating new page...")
	page, err := context.NewPage()
	if err != nil {
		return fmt.Errorf("failed to create new page: %v", err)
	}
	log.Println("✅ New page created")

	log.Println("5. Injecting stealth scripts...")
	if err := page.AddInitScript(playwright.Script{
		Content: playwright.String(`
			Object.defineProperty(navigator, 'webdriver', {
				get: () => undefined
			});
			Object.defineProperty(navigator, 'plugins', {
				get: () => [1, 2, 3, 4, 5]
			});
			Object.defineProperty(navigator, 'languages', {
				get: () => ['en-US', 'en']
			});
		`),
	}); err != nil {
		return fmt.Errorf("failed to inject stealth scripts: %v", err)
	}
	log.Println("✅ Stealth scripts injected")

	const downloadButtonSelector = `button:has-text("Download")`

	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = "."
	}
	downloadPath := filepath.Join(homeDir, "freepik_downloads")

	log.Printf("6. Creating download directory: %s", downloadPath)
	if err := os.MkdirAll(downloadPath, os.ModePerm); err != nil {
		return fmt.Errorf("failed to create download directory: %v", err)
	}
	log.Printf("✅ Downloads will be saved to: %s", downloadPath)

	log.Printf("7. Navigating to %s...", targetURL)
	navResult, err := page.Goto(targetURL, playwright.PageGotoOptions{
		Timeout:   playwright.Float(60000),
		WaitUntil: playwright.WaitUntilStateNetworkidle,
	})
	if err != nil {
		return fmt.Errorf("failed to navigate to page: %v", err)
	}
	log.Printf("✅ Navigation successful - Status: %d", navResult.Status())

	time.Sleep(3 * time.Second)

	title, err := page.Title()
	if err != nil {
		return fmt.Errorf("failed to get page title: %v", err)
	}
	log.Printf("📄 Page Title: %s", title)

	if title == "Access Denied" || title == "Just a moment..." {
		return fmt.Errorf("page blocked by anti-bot protection")
	}

	log.Println("8. Looking for download button...")
	buttonVisible, err := page.IsVisible(downloadButtonSelector)
	if err != nil {
		return fmt.Errorf("error checking for download button: %v", err)
	}
	
	if !buttonVisible {
		return fmt.Errorf("download button not found on page")
	}
	log.Println("✅ Download button found")

	log.Println("9. Clicking the 'Download' button...")
	download, err := page.ExpectDownload(func() error {
		return page.Click(downloadButtonSelector, playwright.PageClickOptions{
			Timeout: playwright.Float(30000),
		})
	})
	if err != nil {
		return fmt.Errorf("failed to click download button: %v", err)
	}

	log.Println("⏳ Download initiated, saving file...")
	filename := download.SuggestedFilename()
	saveFileTo := filepath.Join(downloadPath, filename)
	log.Printf("💾 Saving file as: %s", saveFileTo)
	
	if err := download.SaveAs(saveFileTo); err != nil {
		return fmt.Errorf("failed to save file: %v", err)
	}

	if err := download.Failure(); err != nil {
		return fmt.Errorf("download failed during transfer: %v", err)
	}

	if fileInfo, err := os.Stat(saveFileTo); err == nil {
		log.Printf("✅ Success! File saved: %s (Size: %d bytes)", saveFileTo, fileInfo.Size())
	} else {
		log.Printf("⚠️ File saved but cannot verify: %v", err)
	}

	return nil
}

func jsonResponse(w http.ResponseWriter, response DownloadResponse) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
	}
}

func errorResponse(w http.ResponseWriter, message string, statusCode int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(DownloadResponse{
		Success: false,
		Error:   message,
	})
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
