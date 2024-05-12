package main

import (
	"encoding/json"
	"log"
	"net/http"
	"time"
)

// RespondToClient writes a standard JSON response to the http.ResponseWriter.
// message is a description or error message, status is the HTTP status code for the response,
// and result is an optional payload that can be nil if not used.
func RespondToClient(w http.ResponseWriter, message string, status int, result interface{}) {
	response := map[string]interface{}{
		"message": message,
		"status":  status,
	}
	if result != nil {
		response["result"] = result
	}
	jsonResponse, err := json.Marshal(response)
	if err != nil {
		log.Printf("Error marshaling JSON: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(jsonResponse)
}

// Handlers and Timezone Utilities
func getDateTime(r *http.Request, dt time.Time) string {
	loc := setTimezoneLocation(r)
	if !dt.IsZero() {
		return dt.In(loc).Format(time.RFC3339)
	}
	return time.Now().In(loc).Format(time.RFC3339)
}

func setTimezoneLocation(r *http.Request) *time.Location {
	tz := r.URL.Query().Get("tz")
	if tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			return loc
		}
		return time.UTC // Default to UTC if timezone is invalid
	}
	return time.UTC // Default to UTC if no timezone is specified
}
