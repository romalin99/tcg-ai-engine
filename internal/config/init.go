package config

import (
	"log"
	"os"
	"strings"

	"github.com/spf13/viper"
)

// Init 加载配置：fileName 非空时直接用该文件（-f 传入）；
// 否则按 ENV（dev/sit/prod，默认 prod）在 ./config 等路径下搜索 {env}.toml。
func (c *Config) Init(fileName string) {
	viperConfig := viper.New()

	sanitize := func(s string) string {
		s = strings.ReplaceAll(s, "\n", "")
		s = strings.ReplaceAll(s, "\r", "")
		return s
	}

	env := strings.ToLower(os.Getenv("ENV"))
	env = sanitize(env)
	if env == "" {
		env = "prod"
	}

	var configName string
	switch env {
	case "dev":
		configName = "dev.toml"
	case "sit":
		configName = "sit.toml"
	case "prod":
		configName = "prod.toml"
	default:
		log.Fatal("Unknown environment value (allowed: dev, sit, prod)")
	}

	var searchPaths []string

	if fileName != "" {
		viperConfig.SetConfigFile(fileName)
	} else {
		viperConfig.SetConfigName(configName)
		viperConfig.SetConfigType("toml")

		paths := []string{".", "./config", "../config", "../../config"}
		for _, p := range paths {
			viperConfig.AddConfigPath(p)
			searchPaths = append(searchPaths, p)
		}
	}

	wd, _ := os.Getwd()
	log.Printf("Current working directory     : %s", wd)
	log.Printf("Config name to search for     : %s", configName)
	log.Printf("Search paths (in search order): %v", searchPaths)

	// 1st tier：服务基础默认值
	{
		viperConfig.SetDefault("name", "tcg-ai-engine")
		viperConfig.SetDefault("env", env)
		viperConfig.SetDefault("host", "0.0.0.0")
		viperConfig.SetDefault("port", 18080)
		viperConfig.SetDefault("timeout", 30)           // In second
		viperConfig.SetDefault("bodyLimit", 10485760*5) // 10M * 5
		viperConfig.SetDefault("shutdownTimeout", 10)   // In second
	}

	// 2nd tier：路由超时档位
	{
		viperConfig.SetDefault("timeouts.quick", 5)
		viperConfig.SetDefault("timeouts.normal", 30)
		viperConfig.SetDefault("timeouts.long", 60)
		viperConfig.SetDefault("timeouts.upload", 120)
	}

	// 3rd tier：telemetry / pprof
	{
		viperConfig.SetDefault("telemetry.sampler", 1.0)
		viperConfig.SetDefault("telemetry.batcher", "none")
		viperConfig.SetDefault("pprof.enabled", false)
		viperConfig.SetDefault("pprof.port", 6060)
		viperConfig.SetDefault("pprof.host", "0.0.0.0")
	}

	// 4th tier：日志
	{
		viperConfig.SetDefault("log.mode", "console") // console | file
		viperConfig.SetDefault("log.env", env)
		viperConfig.SetDefault("log.level", "info")
		viperConfig.SetDefault("log.name", "tcg-ai-engine")
		viperConfig.SetDefault("log.serviceName", "TCG-AI-ENGINE")
		viperConfig.SetDefault("log.encoding", "json")
		viperConfig.SetDefault("log.timeFormat", "2006-01-02 15:04:05.000")
		viperConfig.SetDefault("log.path", "logs")
		viperConfig.SetDefault("log.compress", true)
		viperConfig.SetDefault("log.maxBackups", 10)
		viperConfig.SetDefault("log.maxSize", 500)
		viperConfig.SetDefault("log.keepDays", 5)
		viperConfig.SetDefault("log.rotation", "size")
		viperConfig.SetDefault("log.bufferSize", 30)          // MB
		viperConfig.SetDefault("log.bufferFlushInterval", 50) // ms
	}

	// 5th tier：规则引擎数据源（本项目特有）
	{
		viperConfig.SetDefault("rules.source", "file")
		viperConfig.SetDefault("rules.file.dir", "rules")
		viperConfig.SetDefault("rules.oracle.table", "RISK_RULES")
		viperConfig.SetDefault("rules.reload_interval_seconds", 5)
		viperConfig.SetDefault("rules.grl_trace", false)
	}

	log.Printf("Attempting to load config: %s (env=%s)", configName, env) //nolint:gosec

	if err := viperConfig.ReadInConfig(); err != nil {
		log.Fatalf("Failed to read config:\n"+
			"  error       : %v\n"+
			"  config name : %s\n"+
			"  cwd         : %s\n"+
			"  tried paths : %v",
			err, configName, wd, searchPaths)
	}

	if err := viperConfig.Unmarshal(c); err != nil {
		log.Fatalf("Failed to unmarshal config into struct: %v", err)
	}

	if err := c.ValidateRules(); err != nil {
		log.Fatalf("Invalid rules config: %v", err)
	}

	log.Printf("✅ Config loaded successfully, file: %s", viperConfig.ConfigFileUsed())
}
