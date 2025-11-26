package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/playwright-community/playwright-go"
)

// --- GLOBAL CONFIGURATION ---

// Limit total concurrent browsers (Prevents Server Crash)
const MaxConcurrentDownloads = 4

// Limit jobs per specific user (Prevents Spamming)
const MaxJobsPerUser = 2

// Global variables
var (
	browser          playwright.Browser
	browserContext   playwright.BrowserContext
	contextMux       sync.RWMutex
	isLoggedIn       bool
	currentUserEmail string
)

// Concurrency Controls
var (
	// The Waiting Room (Semaphore)
	downloadSemaphore = make(chan struct{}, MaxConcurrentDownloads)

	// User Limits
	userJobCounts = make(map[string]int)
	jobCountMux   sync.Mutex
)

// ----------------------------

type DownloadRequest struct {
	URL   string `json:"url"`
	Email string `json:"email"`
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
		Headless: playwright.Bool(false), // Set to true for production linux servers
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
		browserContext, err = browser.NewContext(playwright.BrowserNewContextOptions{
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

	go func() {
		// 1. Get Credentials from ENV
		email := getEnv("PREMIUM_EMAIL", "")
		password := getEnv("PREMIUM_PASSWORD", "")

		if email == "" || password == "" {
			log.Println("⚠️ No auto-login credentials found in ENV. Waiting for manual login...")
			return
		}

		// 2. Set the global email variable immediately
		contextMux.Lock()
		currentUserEmail = email
		contextMux.Unlock()

		// 3. Retry Loop
		for {
			// Check if we are already logged in (e.g. from storage state)
			contextMux.RLock()
			alreadyLoggedIn := isLoggedIn
			contextMux.RUnlock()

			if alreadyLoggedIn {
				log.Println("✅ System is ready (Logged in).")
				break // Exit the loop
			}

			log.Println("🔄 Initiating Auto-Login sequence...")

			// Attempt login
			err := performLogin(email, password)
			if err == nil {
				log.Println("✅ Auto-Login Successful!")
				break // Exit the loop
			}

			log.Printf("❌ Auto-Login Failed: %v", err)
			log.Println("⏳ Retrying in 10 seconds...")
			time.Sleep(10 * time.Second) // Wait before retrying
		}
	}()

	port := getEnv("PORT", "8080")
	log.Printf("🚀 Starting download API server on port %s", port)

	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("❌ Failed to start server: %v", err)
	}
}

func createNewContext() {
	log.Println("🆕 Creating new browser context...")
	var err error
	browserContext, err = browser.NewContext(playwright.BrowserNewContextOptions{
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

	contextMux.Lock()
	currentUserEmail = req.Email
	contextMux.Unlock()

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

	page, err := browserContext.NewPage()
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
	if _, err := browserContext.StorageState(storageStateFile); err != nil {
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

	if req.Email == "" {
		errorResponse(w, "Email is required for file storage", http.StatusBadRequest)
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

	// Clean variables (Fixes the syntax error from before)
	email := req.Email

	// --- STEP 1: CHECK USER LIMITS ---
	jobCountMux.Lock()
	count := userJobCounts[email]
	if count >= MaxJobsPerUser {
		jobCountMux.Unlock()
		errorResponse(w, fmt.Sprintf("Too many pending requests. You have %d jobs in progress. Please wait.", count), http.StatusTooManyRequests)
		return
	}

	// Increment their count immediately
	userJobCounts[email]++
	jobCountMux.Unlock()
	// ---------------------------------

	// 1. Generate the URL and Key immediately
	publicURL, r2ObjectKey := generateR2Path(req.URL, req.Email)

	// --- STEP 2: START WORKER ---
	go func(targetURL, key, userEmail string) {
		// ALWAYS Ensure we decrement the user's count when this finishes
		defer func() {
			jobCountMux.Lock()
			userJobCounts[userEmail]--
			if userJobCounts[userEmail] < 0 {
				userJobCounts[userEmail] = 0
			}
			jobCountMux.Unlock()
		}()

		log.Printf("⏳ [Queue] User %s waiting... (%d active)", userEmail, len(downloadSemaphore))

		// Wait for a server slot (Global Semaphore)
		downloadSemaphore <- struct{}{}

		// Release server slot when done
		defer func() { <-downloadSemaphore }()

		log.Printf("▶️ [Worker] Processing %s", targetURL)
		processDownload(targetURL, key)

	}(req.URL, r2ObjectKey, email)

	jsonResponse(w, DownloadResponse{
		Success: true,
		Message: "Request queued.",
		File:    publicURL,
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

func processDownload(targetURL, r2Key string) {
	log.Printf("🚀 Starting download for %s -> Key: %s", targetURL, r2Key)

	// 1. Download locally
	localFilePath, err := runDownload(targetURL)
	if err != nil {
		log.Printf("❌ Download failed for %s: %v", targetURL, err)
		return
	}

	log.Printf("✅ Local download complete: %s", localFilePath)

	// 2. Upload to R2
	if err := uploadToR2(localFilePath, r2Key); err != nil {
		log.Printf("❌ R2 Upload failed: %v", err)
		return
	}

	log.Printf("✅ Upload complete: %s", r2Key)

	// 3. Clean up local file
	os.Remove(localFilePath)
}

func generateR2Path(originalURL, email string) (string, string) {

	// 1. Process Filename
	parsed, _ := url.Parse(originalURL)
	baseName := filepath.Base(parsed.Path)
	ext := filepath.Ext(baseName)
	nameWithoutExt := baseName[0 : len(baseName)-len(ext)]
	parts := strings.Split(nameWithoutExt, "_")
	if len(parts) > 1 {
		nameWithoutExt = strings.Join(parts[:len(parts)-1], "_")
	}
	finalFilename := nameWithoutExt + ".zip"

	// 2. Process Email (Sanitize)
	folderName := email
	folderName = strings.ReplaceAll(folderName, "@", "-at-")
	folderName = strings.ReplaceAll(folderName, ".", "-dot-")

	// 3. Construct Paths
	r2Base := getEnv("R2_URL", "https://storage.stokbro.net")
	r2Base = strings.TrimRight(r2Base, "/")

	fullURL := fmt.Sprintf("%s/%s/%s", r2Base, folderName, finalFilename)
	objectKey := fmt.Sprintf("%s/%s", folderName, finalFilename)

	return fullURL, objectKey
}

func uploadToR2(localPath, objectKey string) error {
	accountId := getEnv("R2_ACCOUNT_ID", "")
	accessKey := getEnv("R2_ACCESS_KEY", "")
	secretKey := getEnv("R2_SECRET_KEY", "")
	bucketName := getEnv("R2_BUCKET_NAME", "")

	// Create S3 Client (R2 is S3 compatible)
	r2Resolver := aws.EndpointResolverWithOptionsFunc(func(service, region string, options ...interface{}) (aws.Endpoint, error) {
		return aws.Endpoint{
			URL: fmt.Sprintf("https://%s.r2.cloudflarestorage.com", accountId),
		}, nil
	})

	cfg, err := config.LoadDefaultConfig(context.TODO(),
		config.WithEndpointResolverWithOptions(r2Resolver),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
		config.WithRegion("auto"),
	)
	if err != nil {
		return err
	}

	client := s3.NewFromConfig(cfg)

	// Open local file
	file, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer file.Close()

	// Upload
	_, err = client.PutObject(context.TODO(), &s3.PutObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String(objectKey),
		Body:   file,
	})

	return err
}

func runDownload(targetURL string) (string, error) {
	contextMux.RLock()
	currentContext := browserContext
	contextMux.RUnlock()

	log.Println("3. Creating new page from logged-in context...")
	page, err := currentContext.NewPage()
	if err != nil {
		return "", fmt.Errorf("failed to create new page: %v", err)
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
		return "", fmt.Errorf("failed to inject stealth scripts: %v", err)
	}

	const downloadButtonSelector = `button:has-text("Download")`

	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = "."
	}
	downloadPath := filepath.Join(homeDir, "freepik_downloads")

	if err := os.MkdirAll(downloadPath, os.ModePerm); err != nil {
		return "", fmt.Errorf("failed to create download directory: %v", err)
	}

	log.Printf("5. Navigating to %s...", targetURL)
	if _, err := page.Goto(targetURL, playwright.PageGotoOptions{
		Timeout:   playwright.Float(60000),
		WaitUntil: playwright.WaitUntilStateNetworkidle,
	}); err != nil {
		return "", fmt.Errorf("failed to navigate to page: %v", err)
	}

	time.Sleep(3 * time.Second)

	title, err := page.Title()
	if err != nil {
		return "", fmt.Errorf("failed to get page title: %v", err)
	}
	if title == "Access Denied" || title == "Just a moment..." {
		return "", fmt.Errorf("page blocked by anti-bot protection")
	}

	log.Println("6. Looking for download button...")
	if visible, _ := page.IsVisible(downloadButtonSelector); !visible {
		return "", fmt.Errorf("download button not found")
	}

	log.Println("7. Clicking the 'Download' button...")
	download, err := page.ExpectDownload(func() error {
		return page.Click(downloadButtonSelector, playwright.PageClickOptions{
			Timeout: playwright.Float(30000),
		})
	})
	if err != nil {
		return "", fmt.Errorf("failed to click download button: %v", err)
	}

	filename := download.SuggestedFilename()
	saveFileTo := filepath.Join(downloadPath, filename)

	if err := download.SaveAs(saveFileTo); err != nil {
		return "", fmt.Errorf("failed to save file: %v", err)
	}

	return saveFileTo, nil
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
