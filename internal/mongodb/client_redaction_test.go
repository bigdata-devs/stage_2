package mongodb

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRedactURIHidesCredentialsAndKeepsDiagnostics(t *testing.T) {
	cases := []struct {
		name    string
		uri     string
		want    string
		secrets []string
	}{
		{
			name: "no credentials",
			uri:  "mongodb://localhost:27017",
			want: "mongodb://localhost:27017",
		},
		{
			name:    "user and password",
			uri:     "mongodb://boogle:s3cr3t@mongo:27017/boogle?authSource=admin",
			want:    "mongodb://boogle:xxxxx@mongo:27017/boogle?authSource=admin",
			secrets: []string{"s3cr3t"},
		},
		{
			name:    "percent-encoded password on an SRV cluster",
			uri:     "mongodb+srv://app:p%40ss%3Aw0rd@cluster0.example.net/?retryWrites=true&w=majority",
			want:    "mongodb+srv://app:xxxxx@cluster0.example.net/?retryWrites=true&w=majority",
			secrets: []string{"p%40ss%3Aw0rd", "p@ss:w0rd"},
		},
		{
			name:    "replica set with several hosts",
			uri:     "mongodb://reader:pw123@h1:27017,h2:27017/boogle?replicaSet=rs0",
			want:    "mongodb://reader:xxxxx@h1:27017,h2:27017/boogle?replicaSet=rs0",
			secrets: []string{"pw123"},
		},
		{
			name: "user without password",
			uri:  "mongodb://reader@mongo:27017/",
			want: "mongodb://reader@mongo:27017/",
		},
		{
			name:    "unescaped @ in password",
			uri:     "mongodb://boogle:p@ss@mongo:27017/",
			want:    "mongodb://boogle:xxxxx@mongo:27017/",
			secrets: []string{"p@ss", "ss@"},
		},
		{
			name:    "unescaped slash in password",
			uri:     "mongodb://boogle:pa/ss@mongo:27017/boogle",
			want:    "mongodb://boogle:xxxxx@mongo:27017/boogle",
			secrets: []string{"pa/ss"},
		},
		{
			name: "AWS session token in authMechanismProperties",
			uri: "mongodb://AKIAEXAMPLE:awsSecretKey@mongo/?authMechanism=MONGODB-AWS" +
				"&authMechanismProperties=AWS_SESSION_TOKEN:tok3n",
			want:    "mongodb://AKIAEXAMPLE:xxxxx@mongo/?authMechanism=MONGODB-AWS&authMechanismProperties=xxxxx",
			secrets: []string{"awsSecretKey", "tok3n"},
		},
		{
			name:    "TLS key password option with ; separators",
			uri:     "mongodb://mongo/?tls=true;tlsCertificateKeyFilePassword=hunter2;appName=boogle",
			want:    "mongodb://mongo/?tls=true;tlsCertificateKeyFilePassword=xxxxx;appName=boogle",
			secrets: []string{"hunter2"},
		},
		{
			name:    "missing scheme",
			uri:     "boogle:s3cr3t@mongo:27017",
			want:    "boogle:xxxxx@mongo:27017",
			secrets: []string{"s3cr3t"},
		},
		{
			name: "@ inside an option value only",
			uri:  "mongodb://mongo/?appName=search@stage2",
			want: "mongodb://mongo/?appName=search@stage2",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := RedactURI(testCase.uri)
			if got != testCase.want {
				t.Errorf("RedactURI(%q) = %q, want %q", testCase.uri, got, testCase.want)
			}
			for _, secret := range testCase.secrets {
				if strings.Contains(got, secret) {
					t.Errorf("RedactURI(%q) = %q still contains %q", testCase.uri, got, secret)
				}
			}
		})
	}
}

// TestConnectErrorsNeverContainThePassword covers both failure paths: the driver rejecting the
// URI (its parse errors quote option values back) and a ping that cannot reach the server.
func TestConnectErrorsNeverContainThePassword(t *testing.T) {
	cases := []struct {
		name       string
		uri        string
		secrets    []string
		wantPrefix string
	}{
		{
			name:       "unreachable server",
			uri:        "mongodb://boogle:s3cr3tP4ss@127.0.0.1:1/?connectTimeoutMS=100",
			secrets:    []string{"s3cr3tP4ss"},
			wantPrefix: "ping mongodb://boogle:xxxxx@127.0.0.1:1/?connectTimeoutMS=100: ",
		},
		{
			name:       "driver quotes a malformed secret option",
			uri:        "mongodb://boogle:s3cr3tP4ss@mongo:27017/?tlsCertificateKeyFilePassword=%zzhunter2",
			secrets:    []string{"s3cr3tP4ss", "hunter2"},
			wantPrefix: "connect to mongodb://boogle:xxxxx@mongo:27017/?tlsCertificateKeyFilePassword=xxxxx: ",
		},
		{
			name:       "driver rejects an unescaped slash in the password",
			uri:        "mongodb://boogle:pa/ss@mongo:27017/",
			secrets:    []string{"pa/ss"},
			wantPrefix: "connect to mongodb://boogle:xxxxx@mongo:27017/: ",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := Connect(context.Background(), Config{URI: testCase.uri, Database: "boogle"}, 300*time.Millisecond)
			if err == nil {
				t.Fatal("Connect succeeded, want an error")
			}
			message := err.Error()
			if !strings.HasPrefix(message, testCase.wantPrefix) {
				t.Errorf("error = %q, want prefix %q", message, testCase.wantPrefix)
			}
			for _, secret := range testCase.secrets {
				if strings.Contains(message, secret) {
					t.Errorf("error %q leaks %q", message, secret)
				}
			}
		})
	}
}

func TestConnectErrorKeepsTheDriverCauseForErrorsIs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Connect(ctx, Config{URI: "mongodb://boogle:s3cr3tP4ss@127.0.0.1:1/", Database: "boogle"}, time.Second)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("errors.Is(%v, context.Canceled) = false, want the cause to stay reachable", err)
	}
	if err != nil && strings.Contains(err.Error(), "s3cr3tP4ss") {
		t.Errorf("error %q leaks the password", err)
	}
}
