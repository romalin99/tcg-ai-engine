package pprof

type Config struct {
	Host    string `mapstructure:"host,default=0.0.0.0"`
	Port    int    `mapstructure:"port,default=6060"`
	Enabled bool   `mapstructure:"enabled,default=false"`
}
