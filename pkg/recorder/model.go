package recorder

import "time"

// RecordedRequest almacena la info que queremos preservar por cada request
type RecordedRequest struct {
    Timestamp time.Time         `yaml:"timestamp"`
    Method    string            `yaml:"method"`
    URL       string            `yaml:"url"`
    Host      string            `yaml:"host,omitempty"`
    Headers   map[string]string `yaml:"headers,omitempty"`
    Cookies   map[string]string `yaml:"cookies,omitempty"`
    Body      string            `yaml:"body,omitempty"`
    Proto     string            `yaml:"proto,omitempty"`
    Note      string            `yaml:"note,omitempty"` // por ejemplo "CONNECT - https tunnel"
}
