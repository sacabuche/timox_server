package main

// --- Shared models ---

// GlobalTimePkg is the reserved package name used to store the child's total
// daily screen-time limit as an app_limits row.
const GlobalTimePkg = "GLOBAL_TIME"

type User struct {
	UUID  string `json:"uuid"`
	Email string `json:"email,omitempty"`
	Name  string `json:"name,omitempty"`
	Role  string `json:"role"`
}
