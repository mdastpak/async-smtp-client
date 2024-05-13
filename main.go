package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/smtp"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/spf13/viper"
)

type Config struct {
	ServerHost       string `mapstructure:"SERVER_HOST"`
	ServerPort       string `mapstructure:"SERVER_PORT"`
	RedisHost        string `mapstructure:"REDIS_HOST"`
	RedisPort        string `mapstructure:"REDIS_PORT"`
	RedisPassword    string `mapstructure:"REDIS_PASSWORD"`
	RedisDB          int    `mapstructure:"REDIS_DB"`
	SMTPServer       string `mapstructure:"SMTP_SERVER"`
	SMTPPort         string `mapstructure:"SMTP_PORT"`
	SMTPUsername     string `mapstructure:"SMTP_USERNAME"`
	SMTPSernderTitle string `mapstructure:"SMTP_SENDER_TITLE"`
	SMTPPassword     string `mapstructure:"SMTP_PASSWORD"`
	SMTPInsecure     bool   `mapstructure:"SMTP_INSECURE"`
}

var (
	ctx    = context.Background()
	rdb    *redis.Client
	config Config
)

// EmailRequest represents an email dispatch request with multiple recipients
type EmailRequest struct {
	UUID      uuid.UUID `json:"uuid,omitempty"`
	To        []string  `json:"to"`
	Cc        []string  `json:"cc,omitempty"`  // Carbon copy recipients
	Bcc       []string  `json:"bcc,omitempty"` // Blind carbon copy recipients
	Subject   string    `json:"subject"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

func LoadConfig(path string) (config Config, err error) {
	viper.AddConfigPath(path)
	viper.SetConfigName("config") // Name of config file (without extension)
	viper.SetConfigType("env")    // REQUIRED if the config file does not have the extension in the name
	viper.AutomaticEnv()          // Read in environment variables that match

	// Set default configurations
	viper.SetDefault("SERVER_PORT", "8080")

	if err := viper.ReadInConfig(); err != nil {
		log.Fatalf("Error reading config file, %s", err)
	}

	err = viper.Unmarshal(&config)
	return
}

// main sets up the HTTP server and routes
func main() {

	config_, err := LoadConfig(".")
	if err != nil {
		log.Fatalf("Error loading configuration: %v", err)
	}
	config = config_

	setupRedis()

	go EmailSendingWorker()

	r := mux.NewRouter()
	setupRoutes(r)

	startHTTPServer(r)
}

// Route Setup and Server Start-up
func setupRoutes(router *mux.Router) {
	router.HandleFunc("/", welcomeHandler).Methods("GET")
	router.HandleFunc("/submit", PostEmailHandler).Methods("POST")
	router.HandleFunc("/status", GetEmailStatusHandler).Methods("GET")
	router.HandleFunc("/health", healthCheck).Methods("GET")
}

func setupRedis() {
	rdb = redis.NewClient(&redis.Options{
		Addr:     config.RedisHost + ":" + config.RedisPort,
		Password: config.RedisPassword,
		DB:       config.RedisDB,
	})

	pingRedis()
}

func pingRedis() {
	if _, err := rdb.Ping(ctx).Result(); err != nil {
		log.Fatalf("Failed to connect to Redis: %v", err)
	}
	log.Println("Connected to Redis successfully!")
}

func startHTTPServer(router *mux.Router) {
	server := &http.Server{
		Addr:    config.ServerHost + ":" + config.ServerPort,
		Handler: router,
	}

	go func() {
		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatalf("Failed to start server: %v", err)
		}
	}()
	log.Printf("Server started on %s", server.Addr)

	waitForShutdown(server)
}

func waitForShutdown(server *http.Server) {
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Server is shutting down...")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Fatalf("Could not gracefully shutdown the server: %v", err)
	}
	log.Println("Server shut down.")
}

func welcomeHandler(w http.ResponseWriter, r *http.Request) {

	response := map[string]string{
		"message": "Welcome to the Async Mail Sender Service",
		"dt":      getDateTime(r, time.Now()),
	}
	RespondToClient(w, "Welcome", http.StatusOK, response)
}

func healthCheck(w http.ResponseWriter, r *http.Request) {
	_, redisErr := rdb.Ping(ctx).Result()
	smtpErr := checkSMTPConnection()

	status := "ok"
	if redisErr != nil || smtpErr != nil {
		status = "error"
	}
	healthReport := map[string]interface{}{
		"overall_status": status,
		"redis_status":   checkErr(redisErr),
		"smtp_status":    checkErr(smtpErr),
	}
	RespondToClient(w, "Health check status", http.StatusOK, healthReport)
}

func checkSMTPConnection() error {
	smtpHost := config.SMTPServer
	smtpPort := config.SMTPPort
	conn, err := tls.Dial("tcp", fmt.Sprintf("%s:%s", smtpHost, smtpPort), &tls.Config{
		ServerName:         smtpHost,
		InsecureSkipVerify: config.SMTPInsecure,
	})
	if err != nil {
		return err
	}
	conn.Close()
	return nil
}

func checkErr(err error) string {
	if err != nil {
		return "error"
	}
	return "ok"
}

// PostEmailHandler handles incoming email dispatch requests
func PostEmailHandler(w http.ResponseWriter, r *http.Request) {
	var req EmailRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		RespondToClient(w, "Request decoding failed", http.StatusBadRequest, err.Error())
		return
	}

	// Generate UUID for the new email request
	req.UUID = uuid.New()
	req.CreatedAt = time.Now().UTC()

	// Serialize the email request data for queueing
	emailData, err := json.Marshal(req)
	if err != nil {
		RespondToClient(w, "Failed to serialize email request", http.StatusInternalServerError, err.Error())
		return
	}

	// Push the serialized email data into Redis queue
	if err := rdb.RPush(ctx, "emailQueue", emailData).Err(); err != nil {
		RespondToClient(w, "Failed to enqueue email", http.StatusInternalServerError, err.Error())
		return
	}

	// Successful response with the request details
	RespondToClient(w, "Email registered successfully", http.StatusOK, map[string]interface{}{
		"uuid":   req.UUID,
		"status": "queued",
	})
}

// GetEmailStatusHandler retrieves the status of a dispatched email
func GetEmailStatusHandler(w http.ResponseWriter, r *http.Request) {

	req_uuid := r.URL.Query().Get("uuid")
	if _, err := uuid.Parse(req_uuid); err != nil {
		RespondToClient(w, "Invalid requested UUID", http.StatusBadRequest, nil)
		return
	}

	values, err := rdb.HGetAll(ctx, req_uuid).Result()
	if err != nil {
		RespondToClient(w, "Failed to retrieve status", http.StatusInternalServerError, err.Error())
		return
	}
	if len(values) == 0 {
		RespondToClient(w, "No email found with the given UUID", http.StatusNotFound, nil)
		return
	}

	delete(values, "req") // Delete the "req" key instead of assigning nil

	RespondToClient(w, "Email status retrieved", http.StatusOK, values)
}

func SendEmail(req EmailRequest) error {
	smtpHost := config.SMTPServer
	smtpPort := config.SMTPPort
	from := config.SMTPUsername
	// title := config.SMTPSernderTitle
	password := config.SMTPPassword

	// Set the sender's address using the title from the environment variable
	// sender := fmt.Sprintf("%s <%s>", title, from)

	// TLS configuration
	tlsConfig := &tls.Config{
		ServerName:         smtpHost,
		InsecureSkipVerify: config.SMTPInsecure,
	}

	// Connect to the SMTP server via TLS
	conn, err := tls.Dial("tcp", fmt.Sprintf("%s:%s", smtpHost, smtpPort), tlsConfig)
	if err != nil {
		return fmt.Errorf("failed to connect to SMTP server via TLS: %v", err)
	}
	defer conn.Close()

	client, err := smtp.NewClient(conn, smtpHost)
	if err != nil {
		return fmt.Errorf("failed to create SMTP client: %v", err)
	}
	defer client.Quit()

	// Authenticate and set up the message
	auth := smtp.PlainAuth("", from, password, smtpHost)
	if err := client.Auth(auth); err != nil {
		return fmt.Errorf("failed to authenticate: %v", err)
	}

	// Send MAIL FROM command
	if err := client.Mail(from); err != nil {
		return fmt.Errorf("MAIL FROM command failed: %v", err)
	}

	// Send RCPT TO command for each recipient
	for _, to := range append(req.To, append(req.Cc, req.Bcc...)...) {
		if err := client.Rcpt(to); err != nil {
			return fmt.Errorf("RCPT TO command failed for %s: %v", to, err)
		}
	}

	// Send DATA command
	wc, err := client.Data()
	if err != nil {
		return fmt.Errorf("failed to send DATA command: %v", err)
	}
	defer wc.Close()

	// Write the email body
	message := fmt.Sprintf("From: %s\r\nTo: %s\r\nCc: %s\r\nSubject: %s\r\n\r\n%s",
		from, strings.Join(req.To, ", "), strings.Join(req.Cc, ", "), req.Subject, req.Body)
	if _, err = wc.Write([]byte(message)); err != nil {
		return fmt.Errorf("failed to write email body: %v", err)
	}

	return nil
}

func EmailSendingWorker() {
	for {
		emailData, err := rdb.LPop(ctx, "emailQueue").Result()
		if err == redis.Nil {
			time.Sleep(1 * time.Second) // Wait for 1 second if the queue is empty
			continue
		} else if err != nil {
			log.Printf("Error fetching from queue: %v", err)
			continue
		}

		var req EmailRequest
		if err := json.Unmarshal([]byte(emailData), &req); err != nil {
			log.Printf("Error decoding email request: %v", err)
			continue
		}

		if err := SendEmail(req); err != nil {
			log.Printf("Failed to send email: %v", err)
			// Update Redis with failed status
			rdb.HSet(ctx, req.UUID.String(), map[string]interface{}{
				"status":   "failed",
				"error":    err.Error(),
				"tried_at": time.Now().UTC(),
			})
			continue

		}
		// Update Redis with sent status
		rdb.HSet(ctx, req.UUID.String(), map[string]interface{}{
			"status":     "sent",
			"sent_at":    time.Now().UTC(),
			"created_at": req.CreatedAt,
			"req":        emailData,
		})
		log.Println("Email sent successfully")

	}
}
