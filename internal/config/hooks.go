package config

type HookCommand struct {
	Name         string            `koanf:"name"`
	Cmd          string            `koanf:"cmd"`
	Args         []string          `koanf:"args"`
	Environment  map[string]string `koanf:"environment"`
	AllowFailure bool             `koanf:"allow_failure"`
	StreamOutput bool             `koanf:"stream_output"`
	Disabled     bool             `koanf:"disabled"`
}

type Hooks struct {
	OnSuccess []HookCommand `koanf:"on_success"`
	OnFailure []HookCommand `koanf:"on_failure"`
}
