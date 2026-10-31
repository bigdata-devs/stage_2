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

// DefaultConnectTimeout bounds how long Connect waits for the server to answer its initial
// ping, so a service fails fast at start-up instead of hanging when the database is down.
const DefaultConnectTimeout = 10 * time.Second

// redactedText replaces every credential in a redacted URI or error, the placeholder Go's own
// url.URL.Redacted uses.
const redactedText = "xxxxx"

// Client is a ready-to-use connection to the datamart database. It owns a connection pool that
// is safe for concurrent use, so one Client per process is enough: indexing workers and search
// handlers share it. Close it exactly once when the owning process stops.
type Client struct {
	connection *mongo.Client
	database   *mongo.Database
}

// Connect opens the pool described by config and verifies the server answers a ping within the
// given timeout. A failed ping closes the pool before returning, so no goroutine leaks. Errors
// never carry credentials: services log them as they are, so the URI is redacted and anything
// secret the driver echoes back is scrubbed (see RedactURI).
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

// Close releases the connection pool. The Client must not be used afterwards.
func (client Client) Close(ctx context.Context) error {
	if err := client.connection.Disconnect(ctx); err != nil {
		return fmt.Errorf("disconnect from %s: %w", client.Describe(), err)
	}
	return nil
}

// Ping verifies the server still answers, within the given timeout. Services expose it through
// their health endpoint so the Control Module can tell a ready service from an unreachable one.
func (client Client) Ping(ctx context.Context, timeout time.Duration) error {
	if err := pingWithin(ctx, client.connection, timeout); err != nil {
		return fmt.Errorf("ping %s: %w", client.Describe(), err)
	}
	return nil
}

// Describe renders the configuration as a short label, for logs and health output. It never
// includes credentials, because a URI may embed a user and password.
func (client Client) Describe() string {
	return fmt.Sprintf("mongodb database %q", client.database.Name())
}

// Database exposes the underlying handle so repositories and tests can reach their collections.
func (client Client) Database() *mongo.Database {
	return client.database
}

// Available reports whether a server answers a ping, so callers can skip work that needs a live
// database instead of failing. Tests use it to skip when no server is reachable.
func Available(ctx context.Context, config Config) bool {
	connection, err := mongo.Connect(options.Client().ApplyURI(config.URI))
	if err != nil {
		return false
	}
	defer disconnectQuietly(connection)
	return pingWithin(ctx, connection, DefaultConnectTimeout) == nil
}

// RedactURI returns uri with every credential replaced by "xxxxx", so it can go into a log line
// or an error. It keeps the scheme, user name, hosts, database and harmless options, which is
// what an operator needs to diagnose a failed connection, and hides the password and the value
// of every secret option (authMechanismProperties, which may hold an AWS session token, and any
// option whose name mentions a password, secret or token).
//
// It never fails and errs on the side of hiding too much: user information is taken to end at
// the last "@", so a password with an unescaped "@", "/" or ":" (invalid, but easy to write) is
// still hidden whole, even if that also hides the hosts.
func RedactURI(uri string) string {
	scheme, rest := splitScheme(uri)
	if at := strings.LastIndex(rest, "@"); at != -1 {
		rest = redactUserInfo(rest[:at]) + "@" + rest[at+1:]
	}
	return scheme + redactOptions(rest)
}

// redactedError is a driver failure whose message has been stripped of credentials. Unwrap still
// returns the original error, so errors.Is and errors.As keep working for callers; only code that
// unwraps it and prints the cause explicitly can see the raw driver text.
type redactedError struct {
	message string
	cause   error
}

func (err redactedError) Error() string { return err.message }

func (err redactedError) Unwrap() error { return err.cause }

// connectionError reports a failed step against uri without leaking credentials: the URI is
// redacted, and every secret the driver may have echoed (it quotes hosts, the database and bad
// option values in its parse errors) is removed from the driver's message.
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

// redactUserInfo keeps the user name, which identifies the account without unlocking it, and
// hides the password. A "user name" holding "/" or "?" means the last "@" sat in the path or
// options, so the user information cannot be told apart and is hidden whole.
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

// redactOptions hides the value of every secret option in the query string, keeping the
// separators ("&" or ";") and every other option as written.
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

// uriSecrets lists the credentials in uri in every form the driver might echo them: the password
// under the driver's reading (user information ends at the first "@") and under RedactURI's
// (the last "@"), plus the value of every secret option, each raw and percent-decoded. Longer
// secrets come first, so one that contains another is replaced whole.
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

// isSecretOption reports whether a connection-string option carries a credential. Keys are
// compared percent-decoded and case-insensitively, as the driver reads them.
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
