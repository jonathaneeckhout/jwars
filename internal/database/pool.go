// Package database configures PostgreSQL connections for local and Azure deployments.
package database

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const postgresScope = "https://ossrdbms-aad.database.windows.net/.default"

// NewPool uses DATABASE_AUTH to select password or Azure managed identity auth.
// With managed identity, databaseURL contains connection details without a password.
func NewPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		// Parse errors can include the original connection string and its password.
		return nil, errors.New("invalid PostgreSQL connection configuration")
	}
	switch os.Getenv("DATABASE_AUTH") {
	case "", "password":
	case "azure-managed-identity":
		options := &azidentity.ManagedIdentityCredentialOptions{}
		if clientID := os.Getenv("AZURE_CLIENT_ID"); clientID != "" {
			options.ID = azidentity.ClientID(clientID)
		}
		credential, err := azidentity.NewManagedIdentityCredential(options)
		if err != nil {
			return nil, fmt.Errorf("configure database managed identity: %w", err)
		}
		if err := configureManagedIdentity(config, credential); err != nil {
			return nil, err
		}
	default:
		return nil, errors.New("DATABASE_AUTH must be password or azure-managed-identity")
	}
	return pgxpool.NewWithConfig(ctx, config)
}

func configureManagedIdentity(config *pgxpool.Config, credential azcore.TokenCredential) error {
	conn := config.ConnConfig
	if conn.TLSConfig == nil || conn.TLSConfig.InsecureSkipVerify {
		return errors.New("database managed identity requires verified TLS; use sslmode=verify-full")
	}
	for _, fallback := range conn.Fallbacks {
		if fallback.TLSConfig == nil || fallback.TLSConfig.InsecureSkipVerify {
			return errors.New("database managed identity must not fall back to unverified connections; use sslmode=verify-full")
		}
	}
	conn.Password = ""
	config.BeforeConnect = func(ctx context.Context, conn *pgx.ConnConfig) error {
		// The SDK caches usable tokens and refreshes them as needed. Request a
		// token for every new physical connection, not just once at startup.
		token, err := credential.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{postgresScope}})
		if err != nil {
			return fmt.Errorf("get PostgreSQL access token: %w", err)
		}
		conn.Password = token.Token
		return nil
	}
	return nil
}
