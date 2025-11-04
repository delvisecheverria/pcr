
package format

type Scenario struct {
	Name        string            `yaml:"name"`
	Concurrency int               `yaml:"concurrency,omitempty"`
	RPS         int               `yaml:"rps,omitempty"`
	Duration    string            `yaml:"duration,omitempty"`
	Variables   map[string]string `yaml:"variables,omitempty"`
	Feeders     map[string]string `yaml:"feeders,omitempty"` // name -> path
	Steps       []Step            `yaml:"steps"`
}

type Step struct {
	Name        string            `yaml:"name"`
	Method      string            `yaml:"method"`
	URL         string            `yaml:"url"`
	Headers     map[string]string `yaml:"headers,omitempty"`
	Body        map[string]any    `yaml:"body,omitempty"`
	Extract     map[string]string `yaml:"extract,omitempty"` // name -> jsonpath/regex/header:
	Expect      *Expect           `yaml:"expect,omitempty"`
	ThinkTimeMs int               `yaml:"think_time_ms,omitempty"`
}

type Expect struct {
	Status       int      `yaml:"status,omitempty"`
	BodyContains []string `yaml:"body_contains,omitempty"`
}
