package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jojo/jwars/internal/accounts"
	"github.com/jojo/jwars/internal/database"
	"golang.org/x/term"
)

func main() {
	var err error
	switch {
	case len(os.Args) == 2 && os.Args[1] == "create-admin":
		err = createAdmin()
	case len(os.Args) == 3 && os.Args[1] == "reset-admin-password":
		err = resetAdminPassword(os.Args[2])
	default:
		fmt.Fprintln(os.Stderr, "usage: jwars-admin create-admin | jwars-admin reset-admin-password <username>")
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func createAdmin() error {
	reader := bufio.NewReader(os.Stdin)
	username, err := readLine(reader, "Admin username: ")
	if err != nil {
		return err
	}
	displayName, err := readLine(reader, "Public display name: ")
	if err != nil {
		return err
	}
	password, err := readPassword("Admin password (10–128 characters): ")
	if err != nil {
		return err
	}
	confirm, err := readPassword("Confirm password: ")
	if err != nil {
		return err
	}
	if password != confirm {
		return errors.New("passwords did not match")
	}
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgres://jwars:jwars@127.0.0.1:5432/jwars?sslmode=disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := database.NewPool(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connect to PostgreSQL: %w", err)
	}
	defer pool.Close()
	store := accounts.NewStore(pool)
	account, err := store.CreateAdmin(ctx, username, password, displayName)
	if errors.Is(err, accounts.ErrAdminExists) {
		return errors.New("an admin account already exists; refusing to create another through initial setup")
	}
	if err != nil {
		return fmt.Errorf("create admin account: %w", err)
	}
	fmt.Printf("Created admin account %q for player %s.\n", account.Username, account.PlayerID)
	return nil
}

func resetAdminPassword(username string) error {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgres://jwars:jwars@127.0.0.1:5432/jwars?sslmode=disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := database.NewPool(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connect to PostgreSQL: %w", err)
	}
	defer pool.Close()
	temporary, err := accounts.NewStore(pool).ResetAdminPassword(ctx, username)
	if err != nil {
		return fmt.Errorf("reset admin password: %w", err)
	}
	fmt.Printf("Temporary password for admin %q: %s\nIt must be changed at the next sign-in.\n", username, temporary)
	return nil
}

func readLine(reader *bufio.Reader, prompt string) (string, error) {
	fmt.Print(prompt)
	value, err := reader.ReadString('\n')
	if err != nil && len(value) == 0 {
		return "", err
	}
	return strings.TrimSpace(value), nil
}

func readPassword(prompt string) (string, error) {
	fmt.Print(prompt)
	value, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return "", fmt.Errorf("read password without echo: %w", err)
	}
	return string(value), nil
}
