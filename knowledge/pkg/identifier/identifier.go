package identifier

import ulid "github.com/gaucho-racing/ulid-go"

type Prefix string

const (
	Document           Prefix = "document"
	Source             Prefix = "source"
	SourceIdentity     Prefix = "sourceidentity"
	SourceRecord       Prefix = "sourcerecord"
	Episode            Prefix = "episode"
	EpisodeRecord      Prefix = "episoderecord"
	EpisodeParticipant Prefix = "episodeparticipant"
	Changeset          Prefix = "changeset"
	ChangesetFile      Prefix = "changesetfile"
	Entity             Prefix = "ent"
	Conversation       Prefix = "conversation"
	Session            Prefix = "session"
	Message            Prefix = "message"
	User               Prefix = "usr"
	ServiceAccount     Prefix = "sa"
)

func New(prefix Prefix) string {
	return ulid.Make().Prefixed(string(prefix))
}

func IsValid(value string, prefix Prefix) bool {
	parsedPrefix, parsed, err := ulid.ParsePrefixed(value)
	return err == nil && parsedPrefix == string(prefix) && parsed.Prefixed(parsedPrefix) == value
}
