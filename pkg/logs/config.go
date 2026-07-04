// Package logs provides a structured, zap-based logger with main log and
// optional behavior log (API request logs) output.
package logs

// Config holds all configuration for the logger.
type Config struct {
	FileInfo struct {
		Path         string `mapstructure:"path"`         // Main log directory
		PathBehavior string `mapstructure:"pathBehavior"` // Behavior log directory (API requests)
	} `mapstructure:"fileInfo,optional"`
	Mode                string `mapstructure:"mode"` // "file" or "console"
	Env                 string `mapstructure:"env"`
	Level               string `mapstructure:"level"`       // debug, info, warn, error
	Name                string `mapstructure:"name"`        // Log file base name
	ServiceName         string `mapstructure:"serviceName"` // Tag in log output
	Encoding            string `mapstructure:"encoding"`
	TimeFormat          string `mapstructure:"timeFormat"`
	Path                string `mapstructure:"path"`
	Rotation            string `mapstructure:"rotation"`
	KeepDays            int    `mapstructure:"keepDays"` // Max age in days
	MaxSize             int    `mapstructure:"maxSize"`  // Max size per file (MB)
	MaxBackups          int    `mapstructure:"maxBackups"`
	BufferSize          int    `mapstructure:"bufferSize"`          // Buffer size (MB)
	BufferFlushInterval int    `mapstructure:"bufferFlushInterval"` // Flush interval (ms)
	Stat                bool   `mapstructure:"stat"`
	Compress            bool   `mapstructure:"compress"`
}
