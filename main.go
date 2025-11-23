package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/playwright-community/playwright-go"
)

// Global variables
var (
	browser    playwright.Browser
	context    playwright.BrowserContext
	contextMux sync.RWMutex
	isLoggedIn bool
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

type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type LoginResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Error   string `json:"error,omitempty"`
}

func main() {
	log.Println("1. Starting Playwright Core...")
	pw, err := playwright.Run()
	if err != nil {
		log.Fatalf("❌ Failed to start Playwright: %v", err)
	}

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

	// Try to load existing storage state
	storageStateFile := getStorageStatePath()
	if _, err := os.Stat(storageStateFile); err == nil {
		log.Println("📁 Loading existing storage state...")
		context, err = browser.NewContext(playwright.BrowserNewContextOptions{
			StorageStatePath: playwright.String(storageStateFile),
		})
		if err != nil {
			log.Printf("⚠️ Failed to load storage state: %v", err)
			createNewContext()
		} else {
			isLoggedIn = true
			log.Println("✅ Storage state loaded successfully")
		}
	} else {
		createNewContext()
	}

	http.HandleFunc("/download", downloadHandler)
	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/login", loginHandler)
	http.HandleFunc("/status", statusHandler)

	port := getEnv("PORT", "8080")
	log.Printf("🚀 Starting download API server on port %s", port)

	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("❌ Failed to start server: %v", err)
	}
}

func createNewContext() {
	log.Println("🆕 Creating new browser context...")
	var err error
	context, err = browser.NewContext(playwright.BrowserNewContextOptions{
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
		log.Fatalf("❌ Failed to create browser context: %v", err)
	}
	isLoggedIn = false
}

func getStorageStatePath() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = "."
	}
	return filepath.Join(homeDir, "freepik_storage_state.json")
}

func loginHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, LoginResponse{Success: false, Error: "Invalid JSON payload"})
		return
	}

	// Use default credentials if not provided
	if req.Email == "" {
		req.Email = "mymymy@gmail.com"
	}
	if req.Password == "" {
		req.Password = "mypassword"
	}

	log.Printf("🔐 Login request for email: %s", req.Email)

	if err := performLogin(req.Email, req.Password); err != nil {
		jsonResponse(w, LoginResponse{Success: false, Error: fmt.Sprintf("Login failed: %v", err)})
		return
	}

	jsonResponse(w, LoginResponse{Success: true, Message: "Login successful"})
}

func performLogin(email, password string) error {
	contextMux.Lock()
	defer contextMux.Unlock()

	log.Println("🌐 Starting login sequence...")

	page, err := context.NewPage()
	if err != nil {
		return fmt.Errorf("failed to create page: %v", err)
	}
	defer page.Close()

	// Inject stealth scripts
	if err := page.AddInitScript(playwright.Script{
		Content: playwright.String(`
			Object.defineProperty(navigator, 'webdriver', { get: () => undefined });
			Object.defineProperty(navigator, 'plugins', { get: () => [1, 2, 3, 4, 5] });
			Object.defineProperty(navigator, 'languages', { get: () => ['en-US', 'en'] });
		`),
	}); err != nil {
		return fmt.Errorf("failed to inject stealth scripts: %v", err)
	}

	// Step 1: Go to Freepik homepage
	log.Println("1. Navigating to Freepik homepage...")
	if _, err := page.Goto("https://www.freepik.com", playwright.PageGotoOptions{
		Timeout:   playwright.Float(30000),
		WaitUntil: playwright.WaitUntilStateNetworkidle,
	}); err != nil {
		return fmt.Errorf("failed to navigate to freepik: %v", err)
	}
	time.Sleep(2 * time.Second)

	// Step 2: Click "Sign In" button
	log.Println("2. Looking for 'Sign In' button...")
	signInSelector := `button:has-text("Sign In"), a:has-text("Sign In"), [data-testid="login-button"]`
	if err := page.Click(signInSelector, playwright.PageClickOptions{
		Timeout: playwright.Float(10000),
	}); err != nil {
		return fmt.Errorf("failed to click sign in button: %v", err)
	}
	time.Sleep(3 * time.Second)

	// Step 3: Click "Continue with email" button
	log.Println("3. Looking for 'Continue with email' button...")
	continueWithEmailSelector := `button:has-text("Continue with email"), button:has-text("Log in with email")`
	if err := page.Click(continueWithEmailSelector, playwright.PageClickOptions{
		Timeout: playwright.Float(10000),
	}); err != nil {
		return fmt.Errorf("failed to click continue with email button: %v", err)
	}
	time.Sleep(2 * time.Second)

	// Step 4: Fill email and password
	log.Println("4. Filling login form...")

	// Fill email
	emailSelector := `input[type="email"], input[name="email"], #email`
	if err := page.Fill(emailSelector, email, playwright.PageFillOptions{
		Timeout: playwright.Float(10000),
	}); err != nil {
		return fmt.Errorf("failed to fill email: %v", err)
	}
	time.Sleep(1 * time.Second)

	// Fill password
	passwordSelector := `input[type="password"], input[name="password"], #password`
	if err := page.Fill(passwordSelector, password, playwright.PageFillOptions{
		Timeout: playwright.Float(10000),
	}); err != nil {
		return fmt.Errorf("failed to fill password: %v", err)
	}
	time.Sleep(1 * time.Second)

	// Step 5: Check "Stay logged in" checkbox
	log.Println("5. Checking 'Stay logged in' checkbox...")
	stayLoggedInSelector := `input[type="checkbox"][name="remember"], input[type="checkbox"]#remember, .remember-me input`
	if visible, _ := page.IsVisible(stayLoggedInSelector); visible {
		if err := page.Check(stayLoggedInSelector, playwright.PageCheckOptions{
			Timeout: playwright.Float(5000),
		}); err != nil {
			log.Printf("⚠️ Could not check 'stay logged in' checkbox: %v", err)
		}
	}
	time.Sleep(1 * time.Second)

	// Step 6: Click "Log in" button
	log.Println("6. Clicking 'Log in' button...")
	loginButtonSelector := `button[type="submit"]:has-text("Log in"), button:has-text("Login"), input[type="submit"][value="Log in"]`
	if err := page.Click(loginButtonSelector, playwright.PageClickOptions{
		Timeout: playwright.Float(10000),
	}); err != nil {
		return fmt.Errorf("failed to click login button: %v", err)
	}

	// Step 7: Wait for login to complete
	log.Println("7. Waiting for login to complete...")
	time.Sleep(5 * time.Second)

	// Verify login was successful by checking if we're redirected away from login page
	currentURL := page.URL()
	log.Printf("Current URL after login: %s", currentURL)

	// Check for login success indicators
	loginSuccess := false
	successSelectors := []string{
		`[data-testid="user-avatar"]`,
		`img[alt*="avatar"]`,
		`.user-avatar`,
		`button:has-text("Log out")`,
	}

	for _, selector := range successSelectors {
		if visible, _ := page.IsVisible(selector); visible {
			loginSuccess = true
			break
		}
	}

	if !loginSuccess {
		// Check if we're still on a login page
		if page.URL() == "https://www.freepik.com/login" || page.URL() == "https://www.freepik.com/sign-in" {
			return fmt.Errorf("login failed - still on login page")
		}
		// Try to detect error messages
		errorSelectors := []string{
			`.error-message`,
			`[role="alert"]`,
			`text=Invalid`,
			`text=incorrect`,
			`text=error`,
		}
		for _, selector := range errorSelectors {
			if visible, _ := page.IsVisible(selector); visible {
				return fmt.Errorf("login failed - error message detected")
			}
		}
	}

	// Step 8: Save storage state
	log.Println("8. Saving storage state...")
	storageStateFile := getStorageStatePath()
	if _, err := context.StorageState(storageStateFile); err != nil {
		return fmt.Errorf("failed to save storage state: %v", err)
	}

	isLoggedIn = true
	log.Println("✅ Login successful and storage state saved!")
	return nil
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

	// Check if we're logged in
	contextMux.RLock()
	loggedIn := isLoggedIn
	contextMux.RUnlock()

	if !loggedIn {
		errorResponse(w, "Not logged in. Please login first.", http.StatusUnauthorized)
		return
	}

	go processDownload(req.URL)

	jsonResponse(w, DownloadResponse{
		Success: true,
		Message: "Download started in background",
	})
}

func statusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	contextMux.RLock()
	defer contextMux.RUnlock()

	jsonResponse(w, map[string]interface{}{
		"logged_in": isLoggedIn,
		"status":    "ready",
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
	contextMux.RLock()
	currentContext := context
	contextMux.RUnlock()

	log.Println("3. Creating new page from logged-in context...")
	page, err := currentContext.NewPage()
	if err != nil {
		return fmt.Errorf("failed to create new page: %v", err)
	}
	defer page.Close()

	log.Println("4. Injecting stealth scripts...")
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

	log.Printf("5. Navigating to %s...", targetURL)
	if _, err := page.Goto(targetURL, playwright.PageGotoOptions{
		Timeout:   playwright.Float(60000),
		WaitUntil: playwright.WaitUntilStateNetworkidle,
	}); err != nil {
		return fmt.Errorf("failed to navigate to page: %v", err)
	}

	time.Sleep(3 * time.Second)

	title, err := page.Title()
	if err != nil {
		return fmt.Errorf("failed to get page title: %v", err)
	}
	if title == "Access Denied" || title == "Just a moment..." {
		return fmt.Errorf("page blocked by anti-bot protection")
	}

	log.Println("6. Looking for download button...")
	if visible, _ := page.IsVisible(downloadButtonSelector); !visible {
		return fmt.Errorf("download button not found")
	}

	log.Println("7. Clicking the 'Download' button...")
	download, err := page.ExpectDownload(func() error {
		return page.Click(downloadButtonSelector, playwright.PageClickOptions{
			Timeout: playwright.Float(30000),
		})
	})
	if err != nil {
		return fmt.Errorf("failed to click download button: %v", err)
	}

	filename := download.SuggestedFilename()
	saveFileTo := filepath.Join(downloadPath, filename)

	if err := download.SaveAs(saveFileTo); err != nil {
		return fmt.Errorf("failed to save file: %v", err)
	}

	return nil
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	jsonResponse(w, DownloadResponse{Success: true, Message: "Download API is running"})
}

func jsonResponse(w http.ResponseWriter, response interface{}) {
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
