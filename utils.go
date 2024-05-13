package main

import (
	"encoding/json"
	"net/http"
	"time"
)

// RespondToClient writes a standard JSON response to the http.ResponseWriter.
// message is a description or error message, status is the HTTP status code for the response,
// and data is an optional payload that can be nil if not used.
func RespondToClient(w http.ResponseWriter, message string, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"message": message,
		"data":    data,
	})
}

// getDateTime returns the current time or provided time in the requested timezone from the HTTP request.
// It defaults to UTC if no or an invalid timezone is specified.
func getDateTime(r *http.Request, dt time.Time) string {
	tz := r.URL.Query().Get("tz") // Retrieve timezone from URL query if provided
	location := time.UTC          // Default timezone is UTC

	if tz != "" {
		loc, err := time.LoadLocation(tz)
		if err == nil {
			location = loc // Use the parsed timezone
		}
	}

	// If dt is the zero time, use the current time
	if dt.IsZero() {
		dt = time.Now()
	}
	return dt.In(location).Format(time.RFC3339)
}
