package mongodb

import (
	"cmp"
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
)

const DefaultConnectTimeout = 10 * time.Second

const redactedText = "xxxxx"

type Client struct {
	connection *mongo.Client
	database   *mongo.Database
}

func Connect(ctx context.Context, config Config, timeout time.Duration) (Client, error) {
	connection, err := mongo.Connect(options.Client().ApplyURI(config.URI))
	if err != nil {
		return Client{}, connectionError("connect to", config.URI, err)
	}
	if err := pingWithin(ctx, connection, timeout); err != nil {
		disconnectQuietly(connection)
		return Client{}, connectionError("ping", config.URI, err)
	}
	return Client{connection: connection, database: connection.Database(config.Database)}, nil
}

func (client Client) Close(ctx context.Context) error {
	if err := client.connection.Disconnect(ctx); err != nil {
		return fmt.Errorf("disconnect from %s: %w", client.Describe(), err)
	}
	return nil
}

func (client Client) Ping(ctx context.Context, timeout time.Duration) error {
	if err := pingWithin(ctx, client.connection, timeout); err != nil {
		return fmt.Errorf("ping %s: %w", client.Describe(), err)
	}
	return nil
}

func (client Client) Describe() string {
	return fmt.Sprintf("mongodb database %q", client.database.Name())
}

func (client Client) Database() *mongo.Database {
	return client.database
}

func Available(ctx context.Context, config Config) bool {
	connection, err := mongo.Connect(options.Client().ApplyURI(config.URI))
	if err != nil {
		return false
	}
	defer disconnectQuietly(connection)
	return pingWithin(ctx, connection, DefaultConnectTimeout) == nil
}

func RedactURI(uri string) string {
	scheme, rest := splitScheme(uri)
	if at := strings.LastIndex(rest, "@"); at != -1 {
		rest = redactUserInfo(rest[:at]) + "@" + rest[at+1:]
	}
	return scheme + redactOptions(rest)
}

type redactedError struct {
	message string
	cause   error
}

func (err redactedError) Error() string { return err.message }

func (err redactedError) Unwrap() error { return err.cause }

func connectionError(step string, uri string, cause error) error {
	detail := cause.Error()
	for _, secret := range uriSecrets(uri) {
		detail = strings.ReplaceAll(detail, secret, redactedText)
	}
	return redactedError{message: fmt.Sprintf("%s %s: %s", step, RedactURI(uri), detail), cause: cause}
}

func splitScheme(uri string) (scheme string, rest string) {
	if index := strings.Index(uri, "://"); index != -1 {
		return uri[:index+len("://")], uri[index+len("://"):]
	}
	return "", uri
}

func redactUserInfo(userInfo string) string {
	username, _, hasPassword := strings.Cut(userInfo, ":")
	switch {
	case !hasPassword:
		return userInfo
	case strings.ContainsAny(username, "/?"):
		return redactedText
	default:
		return username + ":" + redactedText
	}
}

func redactOptions(rest string) string {
	path, query, hasQuery := strings.Cut(rest, "?")
	if !hasQuery {
		return rest
	}
	var redacted strings.Builder
	redacted.WriteString(path + "?")
	start := 0
	for index := 0; index <= len(query); index++ {
		if index < len(query) && !isOptionSeparator(rune(query[index])) {
			continue
		}
		redacted.WriteString(redactOption(query[start:index]))
		if index < len(query) {
			redacted.WriteByte(query[index])
		}
		start = index + 1
	}
	return redacted.String()
}

func redactOption(pair string) string {
	if key, _, hasValue := strings.Cut(pair, "="); hasValue && isSecretOption(key) {
		return key + "=" + redactedText
	}
	return pair
}

func uriSecrets(uri string) []string {
	_, rest := splitScheme(uri)
	secrets := []string{}
	add := func(secret string) {
		if secret == "" {
			return
		}
		secrets = append(secrets, secret)
		if decoded, err := url.PathUnescape(secret); err == nil && decoded != secret {
			secrets = append(secrets, decoded)
		}
		if decoded, err := url.QueryUnescape(secret); err == nil && decoded != secret {
			secrets = append(secrets, decoded)
		}
	}
	for _, at := range []int{strings.Index(rest, "@"), strings.LastIndex(rest, "@")} {
		if at == -1 {
			continue
		}
		if _, password, hasPassword := strings.Cut(rest[:at], ":"); hasPassword {
			add(password)
		}
	}
	if _, query, hasQuery := strings.Cut(rest, "?"); hasQuery {
		for _, pair := range strings.FieldsFunc(query, isOptionSeparator) {
			if key, value, hasValue := strings.Cut(pair, "="); hasValue && isSecretOption(key) {
				add(value)
			}
		}
	}
	slices.SortFunc(secrets, func(first, second string) int {
		return cmp.Or(cmp.Compare(len(second), len(first)), strings.Compare(first, second))
	})
	return slices.Compact(secrets)
}

func isOptionSeparator(character rune) bool {
	return character == '&' || character == ';'
}

func isSecretOption(key string) bool {
	if decoded, err := url.QueryUnescape(key); err == nil {
		key = decoded
	}
	key = strings.ToLower(key)
	return key == "authmechanismproperties" ||
		strings.Contains(key, "password") ||
		strings.Contains(key, "secret") ||
		strings.Contains(key, "token")
}

func pingWithin(ctx context.Context, connection *mongo.Client, timeout time.Duration) error {
	pingContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return connection.Ping(pingContext, readpref.Primary())
}

func disconnectQuietly(connection *mongo.Client) {
	_ = connection.Disconnect(context.Background())
}
