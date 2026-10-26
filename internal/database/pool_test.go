package database

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/jackc/pgx/v5/pgxpool"
)

type testCredential struct {
	calls int
	err   error
	t     *testing.T
}

func (c *testCredential) GetToken(ctx context.Context, options policy.TokenRequestOptions) (azcore.AccessToken, error) {
	c.t.Helper()
	if len(options.Scopes) != 1 || options.Scopes[0] != postgresScope {
		c.t.Fatalf("unexpected token scopes: %v", options.Scopes)
	}
	if err := ctx.Err(); err != nil {
		return azcore.AccessToken{}, err
	}
	c.calls++
	return azcore.AccessToken{Token: fmt.Sprintf("token-%d", c.calls)}, c.err
}

func TestManagedIdentityRequestsTokenForEachNewConnection(t *testing.T) {
	config, err := pgxpool.ParseConfig("postgres://player:old-password@db.example/jwars?sslmode=verify-full")
	if err != nil {
		t.Fatal(err)
	}
	credential := &testCredential{t: t}
	if err := configureManagedIdentity(config, credential); err != nil {
		t.Fatal(err)
	}
	if config.ConnConfig.Password != "" {
		t.Fatal("static password retained in managed identity configuration")
	}
	for i := 1; i <= 2; i++ {
		conn := config.ConnConfig.Copy()
		if err := config.BeforeConnect(context.Background(), conn); err != nil {
			t.Fatal(err)
		}
		if want := fmt.Sprintf("token-%d", i); conn.Password != want {
			t.Fatalf("connection password = %q, want %q", conn.Password, want)
		}
	}
	credential.err = errors.New("identity unavailable")
	if err := config.BeforeConnect(context.Background(), config.ConnConfig.Copy()); !errors.Is(err, credential.err) {
		t.Fatalf("token failure not propagated: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := config.BeforeConnect(ctx, config.ConnConfig.Copy()); !errors.Is(err, context.Canceled) {
		t.Fatalf("context cancellation not propagated: %v", err)
	}
}

func TestManagedIdentityRejectsUnverifiedTLS(t *testing.T) {
	for _, mode := range []string{"disable", "allow", "prefer", "require"} {
		t.Run(mode, func(t *testing.T) {
			config, err := pgxpool.ParseConfig("postgres://player@db.example/jwars?sslmode=" + mode)
			if err != nil {
				t.Fatal(err)
			}
			if err := configureManagedIdentity(config, &testCredential{t: t}); err == nil {
				t.Fatal("accepted unverified TLS")
			}
		})
	}
}

func TestPasswordModeRemainsAvailable(t *testing.T) {
	t.Setenv("DATABASE_AUTH", "password")
	pool, err := NewPool(context.Background(), "postgres://local:local-password@127.0.0.1/jwars?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if pool.Config().BeforeConnect != nil || pool.Config().ConnConfig.Password != "local-password" {
		t.Fatal("password mode unexpectedly changed authentication")
	}
}

func TestUnknownAuthModeIsRejected(t *testing.T) {
	t.Setenv("DATABASE_AUTH", "azure-typo")
	if pool, err := NewPool(context.Background(), "postgres://local@localhost/jwars?sslmode=disable"); err == nil {
		pool.Close()
		t.Fatal("unknown mode silently fell back to password authentication")
	}
}
