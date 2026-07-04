package consul

type Config struct {
	Key   string   `yaml:"key"`
	Hosts []string `yaml:"hosts"`
}
