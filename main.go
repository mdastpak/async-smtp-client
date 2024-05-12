package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/joho/godotenv"
)

var (
	rdb *redis.Client
)

// EmailRequest represents an email dispatch request with multiple recipients
type EmailRequest struct {
	Recipients  []string  `json:"recipients"` // Changed to a slice of strings
	Subject     string    `json:"subject"`
	Body        string    `json:"body"`
	ScheduledAt time.Time `json:"scheduled_at,omitempty"`
}

// EmailStatus represents the status of an email dispatch
type EmailStatus struct {
	Status    string `json:"status"`
	SentAt    string `json:"sent_at,omitempty"`
	Error     string `json:"error,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

func loadEnv() {
	if err := godotenv.Load(); err != nil {
		log.Fatalf("Error loading .env file: %v", err)
	}
	log.Println("Loaded .env file successfully!")
}

// main sets up the HTTP server and routes
func main() {

	loadEnv()
	setupRedis()

	r := mux.NewRouter()
	setupRoutes(r)

	// Start the HTTP server in a goroutine
	server := &http.Server{
		Addr:    os.Getenv("SERVER_HOST") + ":" + os.Getenv("SERVER_PORT"),
		Handler: r,
	}

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Failed to start server: %v", err)
		}
	}()
	log.Printf("Server started on %s", server.Addr)

	// Listen for interrupt signal to gracefully shut down the server
	quit := make(chan os.Signal, 1)
	// Capture all signals that you consider as shutdown (SIGINT, SIGTERM are common)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Server is shutting down...")

	// Create a deadline to wait for.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Fatalf("Could not gracefully shutdown the server: %v", err)
	}
	log.Println("Server shut down.")

}

// Route Setup and Server Start-up
func setupRoutes(router *mux.Router) {

	router.HandleFunc("/", welcomeHandler).Methods("GET")
	router.HandleFunc("/submit", PostEmailHandler).Methods("POST")
	router.HandleFunc("/status", GetEmailStatusHandler).Methods("GET")

}

func setupRedis() {
	redisAddr := os.Getenv("REDIS_HOST") + ":" + os.Getenv("REDIS_PORT")
	redisPassword := os.Getenv("REDIS_PASSWORD")
	redisDB, _ := strconv.Atoi(os.Getenv("REDIS_DB"))

	rdb = redis.NewClient(&redis.Options{
		Addr:     redisAddr,
		Password: redisPassword,
		DB:       redisDB,
	})

	if _, err := rdb.Ping(context.Background()).Result(); err != nil {
		log.Fatalf("Failed to connect to Redis: %v", err)
	}
	log.Println("Connected to Redis successfully!")

}

func welcomeHandler(w http.ResponseWriter, r *http.Request) {

	response := map[string]string{
		"message": "Welcome to the Async Mail Sender Service",
		"dt":      getDateTime(r, time.Now()),
	}
	RespondToClient(w, "Welcome", http.StatusOK, response)
}

// PostEmailHandler handles incoming email dispatch requests
func PostEmailHandler(w http.ResponseWriter, r *http.Request) {
	var req EmailRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		RespondToClient(w, "Request Register Failed.", http.StatusBadRequest, err.Error())
		return
	}
	uuid := uuid.NewString()

	// Serialize recipients array to JSON string for storage
	recipientsData, err := json.Marshal(req.Recipients)
	if err != nil {
		RespondToClient(w, "Request Register Failed.", http.StatusInternalServerError, "Not as a valid JSON array.")
		return
	}

	if recipientsData == nil || len(req.Recipients) == 0 {
		RespondToClient(w, "Request Register Failed.", http.StatusBadRequest, "No recipients provided.")
		return
	}

	// Store in Redis
	now := time.Now().UTC()
	if err := rdb.HSet(r.Context(), uuid, "recipients", recipientsData, "subject", req.Subject, "body", req.Body, "status", "queued", "created_at", now).Err(); err != nil {
		RespondToClient(w, "Request Register Failed.", http.StatusInternalServerError, err.Error())
		return
	}

	response := map[string]interface{}{
		"uuid":       uuid,
		"created_at": getDateTime(r, now),
		"status":     "queued",
	}
	RespondToClient(w, "Request Registered Successfully", http.StatusOK, response)
}

// GetEmailStatusHandler retrieves the status of a dispatched email
func GetEmailStatusHandler(w http.ResponseWriter, r *http.Request) {
	req_uuid := r.URL.Query().Get("uuid")

	if _, err := uuid.Parse(req_uuid); err != nil {
		RespondToClient(w, "Invalid requested UUID", http.StatusBadRequest, nil)
		return
	}

	values, err := rdb.HGetAll(r.Context(), req_uuid).Result()
	if err != nil {
		RespondToClient(w, "Internal Server Error", http.StatusInternalServerError, err.Error())
		return
	}
	if len(values) == 0 {
		RespondToClient(w, "Invalid requested UUID", http.StatusNotFound, nil)
		return
	}

	createdAtTime, _ := time.Parse(time.RFC3339, values["created_at"])
	triedAtTime, _ := time.Parse(time.RFC3339, values["tried_at"])

	status := EmailStatus{
		Status:    values["status"],
		Error:     values["error"],
		CreatedAt: getDateTime(r, createdAtTime),
	}
	if status.Status == "queued" {
		status.Status = "pending"
	}

	if sentAt, ok := values["sent_at"]; ok {
		sentAtTime, _ := time.Parse(time.RFC3339, sentAt)
		status.SentAt = getDateTime(r, sentAtTime)
	}

	response := map[string]string{
		"uuid":       req_uuid,
		"status":     status.Status,
		"created_at": status.CreatedAt,
	}

	if status.Status == "succeeded" || status.Status == "failed" {
		response["sent_at"] = status.SentAt
	}

	if status.Status == "failed" {
		response["error"] = status.Error
		response["tried_at"] = getDateTime(r, triedAtTime)
	}
	RespondToClient(w, "Request Inquiry", http.StatusOK, response)
}
