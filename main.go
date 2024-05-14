package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/smtp"
	"strings"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/spf13/viper"
	"go.uber.org/fx"
)

type Config struct {
	ServerHost      string `mapstructure:"SERVER_HOST"`
	ServerPort      string `mapstructure:"SERVER_PORT"`
	RedisHost       string `mapstructure:"REDIS_HOST"`
	RedisPort       string `mapstructure:"REDIS_PORT"`
	RedisPassword   string `mapstructure:"REDIS_PASSWORD"`
	RedisDB         int    `mapstructure:"REDIS_DB"`
	SMTPServer      string `mapstructure:"SMTP_SERVER"`
	SMTPPort        string `mapstructure:"SMTP_PORT"`
	SMTPUsername    string `mapstructure:"SMTP_USERNAME"`
	SMTPSenderTitle string `mapstructure:"SMTP_SENDER_TITLE"`
	SMTPPassword    string `mapstructure:"SMTP_PASSWORD"`
	SMTPInsecure    bool   `mapstructure:"SMTP_INSECURE"`
}

type EmailRequest struct {
	UUID        uuid.UUID `json:"uuid,omitempty"`
	To          []string  `json:"to"`
	Cc          []string  `json:"cc,omitempty"`
	Bcc         []string  `json:"bcc,omitempty"`
	Subject     string    `json:"subject"`
	Body        string    `json:"body"`
	CreatedAt   time.Time `json:"created_at"`
	ScheduledAt time.Time `json:"scheduled_at,omitempty"`
}

type Application struct {
	config Config
	rdb    *redis.Client
	router *mux.Router
}

func LoadConfig() (Config, error) {
	viper.AddConfigPath(".")
	viper.SetConfigName("config")
	viper.SetConfigType("env")
	viper.AutomaticEnv()

	viper.SetDefault("SERVER_PORT", "8080")

	var config Config
	if err := viper.ReadInConfig(); err != nil {
		log.Printf("Error reading config file: %v", err)
	}

	err := viper.Unmarshal(&config)
	return config, err
}

func NewRedisClient(lc fx.Lifecycle, config Config) *redis.Client {
	rdb := redis.NewClient(&redis.Options{
		Addr:     config.RedisHost + ":" + config.RedisPort,
		Password: config.RedisPassword,
		DB:       config.RedisDB,
	})

	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			_, err := rdb.Ping(context.Background()).Result()
			if err != nil {
				return fmt.Errorf("failed to connect to Redis: %v", err)
			}
			log.Println("Connected to Redis successfully!")
			return nil
		},
		OnStop: func(ctx context.Context) error {
			return rdb.Close()
		},
	})

	return rdb
}

func NewRouter() *mux.Router {
	return mux.NewRouter()
}

func RegisterHandlers(router *mux.Router, app *Application) {
	router.HandleFunc("/", app.welcomeHandler).Methods("GET")
	router.HandleFunc("/submit", app.PostEmailHandler).Methods("POST")
	router.HandleFunc("/status", app.GetEmailStatusHandler).Methods("GET")
	router.HandleFunc("/health", app.healthCheck).Methods("GET")
}

func StartHTTPServer(lc fx.Lifecycle, config Config, router *mux.Router) {
	server := &http.Server{
		Addr:    config.ServerHost + ":" + config.ServerPort,
		Handler: router,
	}

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			go func() {
				if err := server.ListenAndServe(); err != http.ErrServerClosed {
					log.Fatalf("Failed to start server: %v", err)
				}
			}()
			log.Printf("Server started on %s", server.Addr)
			return nil
		},
		OnStop: func(ctx context.Context) error {
			return server.Shutdown(ctx)
		},
	})
}

func (app *Application) welcomeHandler(w http.ResponseWriter, r *http.Request) {
	response := map[string]string{
		"message": "Welcome to the Async Mail Sender Service",
		"dt":      getDateTime(r, time.Now()),
	}
	RespondToClient(w, "Welcome", http.StatusOK, response)
}

func (app *Application) healthCheck(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, redisErr := app.rdb.Ping(ctx).Result()
	smtpErr := app.checkSMTPConnection()

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

func (app *Application) checkSMTPConnection() error {
	smtpHost := app.config.SMTPServer
	smtpPort := app.config.SMTPPort
	conn, err := tls.Dial("tcp", fmt.Sprintf("%s:%s", smtpHost, smtpPort), &tls.Config{
		ServerName:         smtpHost,
		InsecureSkipVerify: app.config.SMTPInsecure,
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

func (app *Application) PostEmailHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req EmailRequest
	res := make(map[string]interface{})
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		RespondToClient(w, "Request decoding failed", http.StatusBadRequest, err.Error())
		return
	}

	if !req.ScheduledAt.IsZero() {
		res["scheduled_at"] = req.ScheduledAt
	}

	req.UUID = uuid.New()
	res["uuid"] = req.UUID
	res["status"] = "queued"
	req.CreatedAt = time.Now().UTC()

	emailData, err := json.Marshal(req)
	if err != nil {
		RespondToClient(w, "Failed to serialize email request", http.StatusInternalServerError, err.Error())
		return
	}

	if err := app.rdb.RPush(ctx, "emailQueue", emailData).Err(); err != nil {
		RespondToClient(w, "Failed to enqueue email", http.StatusInternalServerError, err.Error())
		return
	}

	RespondToClient(w, "Email registered successfully", http.StatusOK, res)
}

func (app *Application) GetEmailStatusHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	reqUUID := r.URL.Query().Get("uuid")
	if _, err := uuid.Parse(reqUUID); err != nil {
		RespondToClient(w, "Invalid requested UUID", http.StatusBadRequest, nil)
		return
	}

	values, err := app.rdb.HGetAll(ctx, reqUUID).Result()
	if err != nil {
		RespondToClient(w, "Failed to retrieve status", http.StatusInternalServerError, err.Error())
		return
	}

	// Check if the email is still in the queue
	emailQueue, err := app.rdb.LRange(ctx, "emailQueue", 0, -1).Result()
	if err != nil {
		RespondToClient(w, "Failed to check email queue", http.StatusInternalServerError, err.Error())
		return
	}

	for _, emailData := range emailQueue {
		var emailReq EmailRequest
		if err := json.Unmarshal([]byte(emailData), &emailReq); err != nil {
			log.Printf("Error decoding email request from queue: %v", err)
			continue
		}

		if emailReq.UUID.String() == reqUUID {
			RespondToClient(w, "Email is still in queue", http.StatusOK, map[string]string{
				"uuid":   reqUUID,
				"status": "queued",
			})
			return
		}
	}

	if len(values) == 0 {
		RespondToClient(w, "No email found with the given UUID", http.StatusNotFound, nil)
		return
	}

	delete(values, "req")
	createdAt, _ := time.Parse(time.RFC3339, values["created_at"])
	values["created_at"] = getDateTime(r, createdAt)

	if scheduledAt, err := time.Parse(time.RFC3339, values["scheduled_at"]); err != nil || scheduledAt.IsZero() {
		delete(values, "scheduled_at")
	}

	if sentAt, err := time.Parse(time.RFC3339, values["sent_at"]); err == nil && !sentAt.IsZero() {
		values["sent_at"] = getDateTime(r, sentAt)
	}

	RespondToClient(w, "Email status retrieved", http.StatusOK, values)
}

func (app *Application) SendEmail(ctx context.Context, req EmailRequest) error {
	smtpHost := app.config.SMTPServer
	smtpPort := app.config.SMTPPort
	from := app.config.SMTPUsername
	password := app.config.SMTPPassword

	tlsConfig := &tls.Config{
		ServerName:         smtpHost,
		InsecureSkipVerify: app.config.SMTPInsecure,
	}

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

	auth := smtp.PlainAuth("", from, password, smtpHost)
	if err := client.Auth(auth); err != nil {
		return fmt.Errorf("failed to authenticate: %v", err)
	}

	if err := client.Mail(from); err != nil {
		return fmt.Errorf("MAIL FROM command failed: %v", err)
	}

	for _, to := range append(req.To, append(req.Cc, req.Bcc...)...) {
		if err := client.Rcpt(to); err != nil {
			return fmt.Errorf("RCPT TO command failed for %s: %v", to, err)
		}
	}

	wc, err := client.Data()
	if err != nil {
		return fmt.Errorf("failed to send DATA command: %v", err)
	}
	defer wc.Close()

	message := fmt.Sprintf("From: %s\r\nTo: %s\r\nCc: %s\r\nSubject: %s\r\n\r\n%s",
		from, strings.Join(req.To, ", "), strings.Join(req.Cc, ", "), req.Subject, req.Body)
	if _, err = wc.Write([]byte(message)); err != nil {
		return fmt.Errorf("failed to write email body: %v", err)
	}

	return nil
}

func EmailSendingWorker(app *Application) {
	for {
		ctx := context.Background()
		emailData, err := app.rdb.BLPop(ctx, 0, "emailQueue").Result()
		if err == redis.Nil {
			time.Sleep(1 * time.Second)
			continue
		} else if err != nil {
			log.Printf("Error fetching from queue: %v", err)
			continue
		}

		var req EmailRequest
		if err := json.Unmarshal([]byte(emailData[1]), &req); err != nil {
			log.Printf("Error decoding email request: %v", err)
			continue
		}

		nowUTC := time.Now().UTC()
		scheduledUTC := req.ScheduledAt.UTC()
		if !scheduledUTC.IsZero() && nowUTC.Before(scheduledUTC) {
			app.rdb.RPush(ctx, "emailQueue", emailData[1])
			time.Sleep(1 * time.Second)
			continue
		}

		recipientsData, err := json.Marshal(req)
		if err != nil {
			log.Printf("Failed to serialize recipients: %v", err)
			continue
		}

		if err := app.SendEmail(ctx, req); err != nil {
			log.Printf("Failed to send email: %v", err)
			if err := app.rdb.HSet(ctx, req.UUID.String(), map[string]interface{}{
				"status":   "failed",
				"error":    err.Error(),
				"tried_at": time.Now().UTC(),
				"req":      recipientsData,
			}).Err(); err != nil {
				log.Printf("Failed to update email status: %v", err)
			}
			continue
		}
		if err := app.rdb.HSet(ctx, req.UUID.String(), map[string]interface{}{
			"status":       "sent",
			"sent_at":      time.Now().UTC(),
			"created_at":   req.CreatedAt,
			"scheduled_at": req.ScheduledAt,
			"req":          recipientsData,
		}).Err(); err != nil {
			log.Printf("Failed to update email status: %v", err)
		}
		log.Println("Email sent successfully")
	}
}

func RespondToClient(w http.ResponseWriter, message string, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"message": message,
		"data":    data,
	})
}

func getDateTime(r *http.Request, dt time.Time) string {
	tz := r.URL.Query().Get("tz")
	location := time.UTC

	if tz != "" {
		loc, err := time.LoadLocation(tz)
		if err == nil {
			location = loc
		}
	}

	if dt.IsZero() {
		dt = time.Now()
	}
	return dt.In(location).Format(time.RFC3339)
}

func main() {
	app := fx.New(
		fx.Provide(
			LoadConfig,
			NewRedisClient,
			NewRouter,
			func(config Config, rdb *redis.Client, router *mux.Router) *Application {
				return &Application{config, rdb, router}
			},
		),
		fx.Invoke(
			RegisterHandlers,
			StartHTTPServer,
			func(lc fx.Lifecycle, app *Application) {
				lc.Append(fx.Hook{
					OnStart: func(ctx context.Context) error {
						go EmailSendingWorker(app)
						return nil
					},
				})
			},
		),
	)

	app.Run()
}
