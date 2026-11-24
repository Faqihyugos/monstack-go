package models

import (
	"errors"
	"strings"
	"time"
)

// AgentStatus represents the online state of a registered monitoring agent.
type AgentStatus string

const (
	AgentStatusOnline  AgentStatus = "online"
	AgentStatusOffline AgentStatus = "offline"
	AgentStatusUnknown AgentStatus = "unknown"
)

// Agent represents a remote MonStack-Go monitoring agent node.
type Agent struct {
	ID         string            `json:"id"`
	Hostname   string            `json:"hostname"`
	IPAddress  string            `json:"ip_address"`
	OS         string            `json:"os"`
	Arch       string            `json:"arch"`
	Version    string            `json:"version"`
	Collectors []string          `json:"collectors"`
	Labels     map[string]string `json:"labels"`
	LastSeenAt time.Time         `json:"last_seen_at"`
	Status     AgentStatus       `json:"status"`
	CreatedAt  time.Time         `json:"created_at"`
}

// Validate checks required fields on Agent.
func (a *Agent) Validate() error {
	if strings.TrimSpace(a.ID) == "" {
		return errors.New("agent id cannot be empty")
	}
	if strings.TrimSpace(a.Hostname) == "" {
		return errors.New("agent hostname cannot be empty")
	}
	if strings.TrimSpace(a.IPAddress) == "" {
		return errors.New("agent ip_address cannot be empty")
	}
	return nil
}
