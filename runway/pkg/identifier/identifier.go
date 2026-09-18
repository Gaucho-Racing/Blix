package identifier

import ulid "github.com/gaucho-racing/ulid-go"

type Prefix string

const (
	Run            Prefix = "run"
	Execution      Prefix = "execution"
	Event          Prefix = "event"
	Message        Prefix = "message"
	Call           Prefix = "call"
	ModelCall      Prefix = "modelcall"
	Worker         Prefix = "worker"
	Outbox         Prefix = "outbox"
	Job            Prefix = "job"
	Notification   Prefix = "notification"
	Entity         Prefix = "ent"
	User           Prefix = "usr"
	ServiceAccount Prefix = "sa"
)

func New(prefix Prefix) string {
	return ulid.Make().Prefixed(string(prefix))
}

func IsValid(value string, prefix Prefix) bool {
	parsedPrefix, parsed, err := ulid.ParsePrefixed(value)
	return err == nil && parsedPrefix == string(prefix) && parsed.Prefixed(parsedPrefix) == value
}

func IsOneOf(value string, prefixes ...Prefix) bool {
	for _, prefix := range prefixes {
		if IsValid(value, prefix) {
			return true
		}
	}
	return false
}
