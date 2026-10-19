package mongodb

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
)

// DefaultConnectTimeout bounds how long Connect waits for the server to answer its initial
// ping, so a service fails fast at start-up instead of hanging when the database is down.
const DefaultConnectTimeout = 10 * time.Second

// Client is a ready-to-use connection to the datamart database. It owns a connection pool that
// is safe for concurrent use, so one Client per process is enough: indexing workers and search
// handlers share it. Close it exactly once when the owning process stops.
type Client struct {
	connection *mongo.Client
	database   *mongo.Database
}

// Connect opens the pool described by config and verifies the server answers a ping within the
// given timeout. A failed ping closes the pool before returning, so no goroutine leaks.
func Connect(ctx context.Context, config Config, timeout time.Duration) (Client, error) {
	connection, err := mongo.Connect(options.Client().ApplyURI(config.URI))
	if err != nil {
		return Client{}, fmt.Errorf("connect to %s: %w", config.URI, err)
	}
	if err := pingWithin(ctx, connection, timeout); err != nil {
		disconnectQuietly(connection)
		return Client{}, fmt.Errorf("ping %s: %w", config.URI, err)
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

func pingWithin(ctx context.Context, connection *mongo.Client, timeout time.Duration) error {
	pingContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return connection.Ping(pingContext, readpref.Primary())
}

func disconnectQuietly(connection *mongo.Client) {
	_ = connection.Disconnect(context.Background())
}
