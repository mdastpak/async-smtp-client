package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gorilla/mux"
	"github.com/joho/godotenv"
)

func loadEnv() {
	if err := godotenv.Load(); err != nil {
		log.Fatalf("Error loading .env file: %v", err)
	}
	log.Println("Loaded .env file successfully!")
}

// main sets up the HTTP server and routes
func main() {

	loadEnv()

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

}

func welcomeHandler(w http.ResponseWriter, r *http.Request) {

	response := map[string]string{
		"message": "Welcome to the Async Mail Sender Service",
		"dt":      getDateTime(r),
	}
	RespondToClient(w, "Welcome", http.StatusOK, response)
}
