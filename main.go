package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/playwright-community/playwright-go"
)

// Global variables
var (
	browser    playwright.Browser
	browserCtx playwright.BrowserContext
	contextMux sync.RWMutex
	isLoggedIn bool
	s3Client   *s3.Client
	uploader   *manager.Uploader
)

type DownloadRequest struct {
	URL string `json:"url"`
}

type DownloadResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	File    string `json:"file,omitempty"`
	Error   string `json:"error,omitempty"`
	R2URL   string `json:"r2_url,omitempty"`
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
	// Initialize R2 uploader
	if err := initR2Client(); err != nil {
		log.Fatalf("❌ Failed to initialize R2 client: %v", err)
	}

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
		browserCtx, err = browser.NewContext(playwright.BrowserNewContextOptions{
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

// initR2Client sets up the S3-compatible client for Cloudflare R2
// This uses AWS SDK v2's Functional Options Pattern for configuration
func initR2Client() error {
	// Get R2 credentials from environment variables
	accountID := getEnv("R2_ACCOUNT_ID", "")
	accessKeyID := getEnv("R2_ACCESS_KEY_ID", "")
	secretAccessKey := getEnv("R2_SECRET_ACCESS_KEY", "")

	if accountID == "" || accessKeyID == "" || secretAccessKey == "" {
		return fmt.Errorf("R2 credentials not set. Set R2_ACCOUNT_ID, R2_ACCESS_KEY_ID, and R2_SECRET_ACCESS_KEY")
	}

	// Construct R2 endpoint
	endpoint := fmt.Sprintf("https://%s.r2.cloudflarestorage.com", accountID)

	// Create custom resolver for R2 endpoint
	// This is the Adapter Pattern - adapting AWS SDK to work with R2
	customResolver := aws.EndpointResolverWithOptionsFunc(
		func(service, region string, options ...interface{}) (aws.Endpoint, error) {
			return aws.Endpoint{
				URL:               endpoint,
				SigningRegion:     "auto",
				HostnameImmutable: true,
			}, nil
		},
	)

	// Load config with custom settings
	// The Functional Options Pattern allows flexible configuration
	cfg, err := config.LoadDefaultConfig(context.TODO(),
		config.WithEndpointResolverWithOptions(customResolver),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			accessKeyID,
			secretAccessKey,
			"", // session token not needed for R2
		)),
		config.WithRegion("auto"), // R2 uses "auto" as region
	)
	if err != nil {
		return fmt.Errorf("failed to load AWS config: %v", err)
	}

	// Create S3 client with R2-specific options
	s3Client = s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.UsePathStyle = true // Required for R2 compatibility
	})

	// Create uploader with custom concurrency settings
	// This is the Builder Pattern - constructing complex upload configurations
	uploader = manager.NewUploader(s3Client, func(u *manager.Uploader) {
		u.PartSize = 10 * 1024 * 1024 // 10MB parts for multipart uploads
		u.Concurrency = 5             // Upload 5 parts concurrently
	})

	log.Println("✅ R2 client initialized successfully")
	return nil
}

// uploadToR2 streams file data directly to R2 without local storage
// This uses the Stream Processing pattern - processing data as it flows
func uploadToR2(reader io.Reader, filename string) (string, error) {
	bucketName := getEnv("R2_BUCKET_NAME", "freepik-downloads")

	// Generate unique key using timestamp to avoid collisions
	timestamp := time.Now().Format("2006-01-02")
	key := fmt.Sprintf("%s/%s", timestamp, filename)

	log.Printf("📤 Uploading to R2: %s/%s", bucketName, key)

	// Upload directly from the reader stream
	// Context pattern for timeout and cancellation control
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	result, err := uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String(key),
		Body:   reader,
	})
	if err != nil {
		return "", fmt.Errorf("failed to upload to R2: %v", err)
	}

	log.Printf("✅ Upload complete: %s", result.Location)
	return result.Location, nil
}

func createNewContext() {
	log.Println("🆕 Creating new browser context...")
	var err error
	browserCtx, err = browser.NewContext(playwright.BrowserNewContextOptions{
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

	page, err := browserCtx.NewPage()
	if err != nil {
		return fmt.Errorf("failed to create page: %v", err)
	}
	defer page.Close()

	if err := page.AddInitScript(playwright.Script{
		Content: playwright.String(`
			Object.defineProperty(navigator, 'webdriver', { get: () => undefined });
			Object.defineProperty(navigator, 'plugins', { get: () => [1, 2, 3, 4, 5] });
			Object.defineProperty(navigator, 'languages', { get: () => ['en-US', 'en'] });
		`),
	}); err != nil {
		return fmt.Errorf("failed to inject stealth scripts: %v", err)
	}

	log.Println("1. Navigating to Freepik homepage...")
	if _, err := page.Goto("https://www.freepik.com", playwright.PageGotoOptions{
		Timeout:   playwright.Float(30000),
		WaitUntil: playwright.WaitUntilStateNetworkidle,
	}); err != nil {
		return fmt.Errorf("failed to navigate to freepik: %v", err)
	}
	time.Sleep(2 * time.Second)

	log.Println("2. Looking for 'Sign In' button...")
	signInSelector := `button:has-text("Sign In"), a:has-text("Sign In"), [data-testid="login-button"]`
	if err := page.Click(signInSelector, playwright.PageClickOptions{
		Timeout: playwright.Float(10000),
	}); err != nil {
		return fmt.Errorf("failed to click sign in button: %v", err)
	}
	time.Sleep(3 * time.Second)

	log.Println("3. Looking for 'Continue with email' button...")
	continueWithEmailSelector := `button:has-text("Continue with email"), button:has-text("Log in with email")`
	if err := page.Click(continueWithEmailSelector, playwright.PageClickOptions{
		Timeout: playwright.Float(10000),
	}); err != nil {
		return fmt.Errorf("failed to click continue with email button: %v", err)
	}
	time.Sleep(2 * time.Second)

	log.Println("4. Filling login form...")
	emailSelector := `input[type="email"], input[name="email"], #email`
	if err := page.Fill(emailSelector, email, playwright.PageFillOptions{
		Timeout: playwright.Float(10000),
	}); err != nil {
		return fmt.Errorf("failed to fill email: %v", err)
	}
	time.Sleep(1 * time.Second)

	passwordSelector := `input[type="password"], input[name="password"], #password`
	if err := page.Fill(passwordSelector, password, playwright.PageFillOptions{
		Timeout: playwright.Float(10000),
	}); err != nil {
		return fmt.Errorf("failed to fill password: %v", err)
	}
	time.Sleep(1 * time.Second)

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

	log.Println("6. Clicking 'Log in' button...")
	loginButtonSelector := `button[type="submit"]:has-text("Log in"), button:has-text("Login"), input[type="submit"][value="Log in"]`
	if err := page.Click(loginButtonSelector, playwright.PageClickOptions{
		Timeout: playwright.Float(10000),
	}); err != nil {
		return fmt.Errorf("failed to click login button: %v", err)
	}

	log.Println("7. Waiting for login to complete...")
	time.Sleep(5 * time.Second)

	currentURL := page.URL()
	log.Printf("Current URL after login: %s", currentURL)

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
		if page.URL() == "https://www.freepik.com/login" || page.URL() == "https://www.freepik.com/sign-in" {
			return fmt.Errorf("login failed - still on login page")
		}
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

	log.Println("8. Saving storage state...")
	storageStateFile := getStorageStatePath()
	if _, err := browserCtx.StorageState(storageStateFile); err != nil {
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
	currentContext := browserCtx
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

	// This uses the Callback Pattern - ExpectDownload takes a function to execute
	download, err := page.ExpectDownload(func() error {
		return page.Click(downloadButtonSelector, playwright.PageClickOptions{
			Timeout: playwright.Float(30000),
		})
	})
	if err != nil {
		return fmt.Errorf("failed to click download button: %v", err)
	}

	filename := download.SuggestedFilename()

	// Create temporary file - this uses the Temporary Resource Pattern
	// The file is automatically cleaned up after upload
	tmpFile, err := os.CreateTemp("", "freepik-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %v", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()          // Close immediately so Playwright can write to it
	defer os.Remove(tmpPath) // Ensure cleanup even if upload fails

	log.Println("8. Saving download to temporary file...")
	if err := download.SaveAs(tmpPath); err != nil {
		return fmt.Errorf("failed to save download: %v", err)
	}

	log.Println("9. Streaming file to R2...")
	// Open file for reading and stream to R2
	file, err := os.Open(tmpPath)
	if err != nil {
		return fmt.Errorf("failed to open temp file: %v", err)
	}
	defer file.Close()

	// Upload from file stream - temp file is deleted after this function returns
	r2URL, err := uploadToR2(file, filename)
	if err != nil {
		return fmt.Errorf("failed to upload to R2: %v", err)
	}

	log.Printf("✅ File uploaded to R2: %s", r2URL)
	log.Printf("🗑️  Temporary file cleaned up: %s", tmpPath)
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
