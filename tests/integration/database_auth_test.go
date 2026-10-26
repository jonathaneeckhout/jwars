package integration_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jojo/jwars/internal/database"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestDatabaseAuthentication(t *testing.T) {
	databaseURL, admin := startAuthDatabase(t)

	for _, identity := range []struct{ name, clientID string }{
		{"user assigned identity", "test-purpose-client-id"},
		{"system assigned identity", ""},
	} {
		t.Run("managed identity authenticates with "+identity.name, func(t *testing.T) {
			useIdentityEndpoint(t, identity.clientID, func(w http.ResponseWriter, r *http.Request) {
				writeIdentityToken(w, "token-one", time.Now().Add(time.Hour))
			})
			pool := openAuthPool(t, databaseURL)
			assertDatabaseIdentity(t, pool)
		})
	}

	t.Run("new connections renew a token near expiry", func(t *testing.T) {
		var renewed atomic.Bool
		useIdentityEndpoint(t, "test-purpose-client-id", func(w http.ResponseWriter, r *http.Request) {
			if renewed.Load() {
				writeIdentityToken(w, "token-two", time.Now().Add(time.Hour))
				return
			}
			// A token inside the SDK's expiry safety window is usable by the
			// first connection but must be renewed on the next connection.
			writeIdentityToken(w, "token-one", time.Now().Add(time.Minute))
		})
		pool := openAuthPool(t, databaseURL)
		assertDatabaseIdentity(t, pool)

		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, `ALTER ROLE "auth player" PASSWORD 'token-two'`); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := admin.Exec(ctx, `ALTER ROLE "auth player" PASSWORD 'token-one'`); err != nil {
				t.Errorf("restore test role password: %v", err)
			}
		})
		renewed.Store(true)
		pool.Reset()
		// This can only succeed with token-two on a new physical connection.
		assertDatabaseIdentity(t, pool)
	})

	t.Run("identity failure cannot fall back to a URL password and can recover", func(t *testing.T) {
		var available atomic.Bool
		useIdentityEndpoint(t, "test-purpose-client-id", func(w http.ResponseWriter, r *http.Request) {
			if !available.Load() {
				http.Error(w, `{"error":"access_denied"}`, http.StatusForbidden)
				return
			}
			writeIdentityToken(w, "token-one", time.Now().Add(time.Hour))
		})
		// The URL contains a working password: success while the identity
		// endpoint is unavailable would reveal an authentication fallback.
		pool := openAuthPool(t, authURL(t, databaseURL, "token-one", nil))
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		if err := pool.Ping(ctx); err == nil || !strings.Contains(err.Error(), "get PostgreSQL access token") {
			t.Fatalf("want token acquisition failure, got %v", err)
		}
		available.Store(true)
		assertDatabaseIdentity(t, pool)
	})

	t.Run("a rejected token cannot fall back to a URL password", func(t *testing.T) {
		useIdentityEndpoint(t, "test-purpose-client-id", func(w http.ResponseWriter, r *http.Request) {
			writeIdentityToken(w, "invalid-token", time.Now().Add(time.Hour))
		})
		pool := openAuthPool(t, authURL(t, databaseURL, "token-one", nil))
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		var postgresError *pgconn.PgError
		if err := pool.Ping(ctx); !errors.As(err, &postgresError) || postgresError.Code != "28P01" {
			t.Fatalf("want PostgreSQL invalid-password error for rejected token, got %v", err)
		}
	})

	for _, mode := range []string{"", "password"} {
		t.Run("password authentication mode="+mode, func(t *testing.T) {
			t.Setenv("DATABASE_AUTH", mode)
			pool := openAuthPool(t, authURL(t, databaseURL, "token-one", nil))
			assertDatabaseIdentity(t, pool)
		})
	}

	t.Run("password authentication rejects an incorrect password", func(t *testing.T) {
		t.Setenv("DATABASE_AUTH", "password")
		pool := openAuthPool(t, databaseURL)
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		var postgresError *pgconn.PgError
		if err := pool.Ping(ctx); !errors.As(err, &postgresError) || postgresError.Code != "28P01" {
			t.Fatalf("want PostgreSQL invalid-password error, got %v", err)
		}
	})

	for _, mode := range []string{"disable", "allow", "prefer", "require"} {
		t.Run("managed identity rejects sslmode="+mode, func(t *testing.T) {
			useIdentityEndpoint(t, "test-purpose-client-id", func(w http.ResponseWriter, r *http.Request) {
				t.Error("identity endpoint must not be called with unverified database TLS")
				http.Error(w, "unexpected token request", http.StatusForbidden)
			})
			pool, err := database.NewPool(t.Context(), authURL(t, databaseURL, "", map[string]string{"sslmode": mode}))
			if pool != nil {
				pool.Close()
			}
			if err == nil || !strings.Contains(err.Error(), "use sslmode=verify-full") {
				t.Fatalf("want verified TLS configuration error, got %v", err)
			}
		})
	}

	t.Run("managed identity rejects an untrusted database certificate", func(t *testing.T) {
		useIdentityEndpoint(t, "test-purpose-client-id", func(w http.ResponseWriter, r *http.Request) {
			writeIdentityToken(w, "token-one", time.Now().Add(time.Hour))
		})
		otherCA, _ := authDatabaseCertificate(t)
		pool := openAuthPool(t, authURL(t, databaseURL, "", map[string]string{"sslrootcert": otherCA}))
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		var unknownCA x509.UnknownAuthorityError
		if err := pool.Ping(ctx); !errors.As(err, &unknownCA) {
			t.Fatalf("want untrusted certificate error, got %v", err)
		}
	})
}

func authURL(t *testing.T, databaseURL, password string, settings map[string]string) string {
	t.Helper()
	u, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword("auth player", password)
	query := u.Query()
	for name, value := range settings {
		query.Set(name, value)
	}
	u.RawQuery = query.Encode()
	return u.String()
}

// This fixture validates token delivery through the actual Azure SDK and pgx
// connection pool. PostgreSQL uses SCRAM with the stub token as its password;
// Azure's validation of Entra tokens and role provisioning are not emulated.
func startAuthDatabase(t *testing.T) (string, *pgxpool.Pool) {
	t.Helper()
	certFile, keyFile := authDatabaseCertificate(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	container, err := postgres.Run(ctx, "postgres:17-alpine",
		postgres.WithDatabase("jwars_auth"),
		postgres.WithUsername("auth_operator"),
		postgres.WithPassword("operator-password"),
		postgres.WithSSLCert(certFile, certFile, keyFile),
		testcontainers.WithCmd("postgres",
			"-c", "ssl=on",
			"-c", "ssl_cert_file=/tmp/testcontainers-go/postgres/server.cert",
			"-c", "ssl_key_file=/tmp/testcontainers-go/postgres/server.key"),
		postgres.BasicWaitStrategies(),
	)
	if container != nil {
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := container.Terminate(ctx); err != nil {
				t.Errorf("terminate authentication database: %v", err)
			}
		})
	}
	if err != nil {
		t.Fatal(err)
	}
	connectionString, err := container.ConnectionString(ctx, "sslmode=verify-full")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(connectionString)
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	query.Set("sslrootcert", certFile)
	query.Set("pool_max_conns", "1")
	u.RawQuery = query.Encode()
	admin, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	if _, err := admin.Exec(ctx, `CREATE ROLE "auth player" LOGIN PASSWORD 'token-one'`); err != nil {
		t.Fatal(err)
	}
	// An intentionally stale URL password ensures identity auth must override it.
	u.User = url.UserPassword("auth player", "stale-url-password")
	return u.String(), admin
}

func authDatabaseCertificate(t *testing.T) (string, string) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	certificate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "jwars integration PostgreSQL"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	key, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "server.pem"), filepath.Join(dir, "server.key")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

func useIdentityEndpoint(t *testing.T, clientID string, handler http.HandlerFunc) {
	t.Helper()
	if clientID != "" {
		// The Azure SDK shares its managed identity token cache between
		// credential instances. Give each scenario its own identity so a
		// cached success cannot mask a later endpoint failure or rotation.
		clientID += "-" + rand.Text()
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet ||
			r.Header.Get("X-IDENTITY-HEADER") != "test-identity-header" ||
			r.URL.Query().Get("client_id") != clientID ||
			r.URL.Query().Get("resource") != "https://ossrdbms-aad.database.windows.net" {
			t.Errorf("invalid managed identity request: %s %s", r.Method, r.URL)
			http.Error(w, "invalid managed identity request", http.StatusBadRequest)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	t.Setenv("DATABASE_AUTH", "azure-managed-identity")
	t.Setenv("AZURE_CLIENT_ID", clientID)
	t.Setenv("IDENTITY_ENDPOINT", server.URL)
	t.Setenv("IDENTITY_HEADER", "test-identity-header")
	// Prevent a developer/CI Azure environment from selecting another platform.
	for _, name := range []string{"IDENTITY_SERVER_THUMBPRINT", "MSI_ENDPOINT", "MSI_SECRET", "IMDS_ENDPOINT"} {
		t.Setenv(name, "")
	}
}

func writeIdentityToken(w http.ResponseWriter, token string, expires time.Time) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": token,
		"expires_on":   expires.Unix(),
		"token_type":   "Bearer",
		"resource":     "https://ossrdbms-aad.database.windows.net",
	})
}

func openAuthPool(t *testing.T, databaseURL string) *pgxpool.Pool {
	t.Helper()
	pool, err := database.NewPool(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func assertDatabaseIdentity(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var user, name string
	if err := pool.QueryRow(ctx, "SELECT current_user, current_database()").Scan(&user, &name); err != nil {
		t.Fatalf("query through authenticated connection: %v", err)
	}
	if user != "auth player" || name != "jwars_auth" {
		t.Fatalf("connected as %q to %q, want auth player / jwars_auth", user, name)
	}
}
