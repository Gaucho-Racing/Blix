package identifier

import ulid "github.com/gaucho-racing/ulid-go"

type Prefix string

const (
	Run                 Prefix = "run"
	Session             Prefix = "session"
	Turn                Prefix = "turn"
	Task                Prefix = "task"
	Attempt             Prefix = "attempt"
	Artifact            Prefix = "artifact"
	Action              Prefix = "action"
	Grant               Prefix = "grant"
	Approval            Prefix = "approval"
	Command             Prefix = "command"
	Workspace           Prefix = "workspace"
	WorkspaceRepository Prefix = "workspacerepo"
	Event               Prefix = "event"
	Message             Prefix = "message"
	Call                Prefix = "call"
	ModelCall           Prefix = "modelcall"
	Worker              Prefix = "worker"
	Outbox              Prefix = "outbox"
	Job                 Prefix = "job"
	Notification        Prefix = "notification"
	Entity              Prefix = "ent"
	User                Prefix = "usr"
	ServiceAccount      Prefix = "sa"
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
