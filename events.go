package main

import (
	"encoding/json"
	"io"
	"net/http"
	"time"
)

const eventTypeUltraSaveEnabled = "ultra_save_enabled"

var knownEventTypes = map[string]bool{
	eventTypeUltraSaveEnabled: true,
}

type deviceEventInput struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Data      json.RawMessage `json:"data"`
}

func (e deviceEventInput) validate() bool {
	if !knownEventTypes[e.Type] {
		return false
	}
	if _, err := time.Parse(time.RFC3339Nano, e.Timestamp); err != nil {
		if _, err2 := time.Parse(time.RFC3339, e.Timestamp); err2 != nil {
			return false
		}
	}
	var data map[string]any
	if err := json.Unmarshal(e.Data, &data); err != nil {
		return false
	}
	return e.validateData(data)
}

func (e deviceEventInput) validateData(data map[string]any) bool {
	switch e.Type {
	case eventTypeUltraSaveEnabled:
		v, ok := data["batteryLevel"]
		if !ok {
			return false
		}
		f, ok := v.(float64)
		return ok && f >= 0 && f <= 100
	}
	return false
}

func handleReportEvents(w http.ResponseWriter, r *http.Request) {
	userUUID := r.Context().Value(CtxUserUUID).(string)
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	var events []deviceEventInput
	if err := json.Unmarshal(body, &events); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	for _, e := range events {
		if !e.validate() {
			continue
		}
		data := "{}"
		if len(e.Data) > 0 {
			data = string(e.Data)
		}
		db.Exec(
			`INSERT INTO device_events (user_uuid, event_type, event_timestamp, data) VALUES (?, ?, ?, ?)`,
			userUUID, e.Type, e.Timestamp, data,
		)
	}
	w.WriteHeader(http.StatusNoContent)
}
