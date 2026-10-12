package accounts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/argon2"
)

const (
	passwordSaltBytes  = 16
	passwordKeyBytes   = 32
	passwordMemory     = 64 * 1024
	passwordIterations = 2
	passwordThreads    = 1
	maxAPITokens       = 10
	sessionLifetime    = 14 * 24 * time.Hour
)

var (
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrSuspended          = errors.New("account is suspended")
	ErrUsernameTaken      = errors.New("username is already taken")
	ErrDisplayNameTaken   = errors.New("display name is already taken")
	ErrInvalidAccount     = errors.New("account details are invalid")
	ErrTooManyTokens      = errors.New("maximum number of API tokens reached")
	ErrTokenNameTaken     = errors.New("an API token already uses that name")
	ErrAdminExists        = errors.New("an admin account already exists")
	usernamePattern       = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{2,31}$`)
)

type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

type Account struct {
	PlayerID           string `json:"player_id"`
	Username           string `json:"username"`
	DisplayName        string `json:"display_name"`
	IsAdmin            bool   `json:"is_admin"`
	Suspended          bool   `json:"suspended"`
	MustChangePassword bool   `json:"must_change_password"`
	SeasonNumber       int64  `json:"season_number"`
	JoinedThisSeason   bool   `json:"joined_this_season"`
}

type Session struct {
	Token     string    `json:"session_token"`
	ExpiresAt time.Time `json:"expires_at"`
	Account   Account   `json:"account"`
}

type APIToken struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	Token     string    `json:"token,omitempty"`
}

type AdminAccount struct {
	PlayerID    string `json:"player_id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Suspended   bool   `json:"suspended"`
	IsAdmin     bool   `json:"is_admin"`
}

func NormalizeUsername(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if !usernamePattern.MatchString(value) {
		return "", ErrInvalidAccount
	}
	return value, nil
}

func ValidatePassword(value string) error {
	if utf8.RuneCountInString(value) < 10 || utf8.RuneCountInString(value) > 128 {
		return ErrInvalidAccount
	}
	return nil
}

func NormalizeDisplayName(value string) (string, error) {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) < 2 || utf8.RuneCountInString(value) > 24 {
		return "", ErrInvalidAccount
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return "", ErrInvalidAccount
		}
	}
	return value, nil
}

func ValidateTokenName(value string) (string, error) {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) < 1 || utf8.RuneCountInString(value) > 40 {
		return "", ErrInvalidAccount
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return "", ErrInvalidAccount
		}
	}
	return value, nil
}

func hashPassword(password string, salt []byte) []byte {
	return argon2.IDKey([]byte(password), salt, passwordIterations, passwordMemory, passwordThreads, passwordKeyBytes)
}

func randomSecret(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func passwordMaterial(password string) ([]byte, []byte, error) {
	if err := ValidatePassword(password); err != nil {
		return nil, nil, err
	}
	salt := make([]byte, passwordSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return nil, nil, err
	}
	return hashPassword(password, salt), salt, nil
}

func (s *Store) CreateAccount(ctx context.Context, username, password, displayName string, isAdmin bool) (Account, error) {
	username, err := NormalizeUsername(username)
	if err != nil {
		return Account{}, err
	}
	displayName, err = NormalizeDisplayName(displayName)
	if err != nil {
		return Account{}, err
	}
	passwordHash, salt, err := passwordMaterial(password)
	if err != nil {
		return Account{}, err
	}
	playerID, err := newPlayerID()
	if err != nil {
		return Account{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Account{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `INSERT INTO players(player_id) VALUES($1)`, playerID); err != nil {
		return Account{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO player_accounts(player_id,username,password_hash,password_salt,display_name,is_admin) VALUES($1,$2,$3,$4,$5,$6)`, playerID, username, passwordHash, salt, displayName, isAdmin)
	if err != nil {
		return Account{}, accountConstraintError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Account{}, err
	}
	return Account{PlayerID: playerID, Username: username, DisplayName: displayName, IsAdmin: isAdmin}, nil
}

func accountConstraintError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		switch pgErr.ConstraintName {
		case "player_accounts_username_lower_idx":
			return ErrUsernameTaken
		case "player_accounts_display_name_lower_idx":
			return ErrDisplayNameTaken
		case "api_tokens_player_name_idx":
			return ErrTokenNameTaken
		}
	}
	return err
}

func (s *Store) Login(ctx context.Context, username, password string) (Session, error) {
	username, err := NormalizeUsername(username)
	if err != nil {
		return Session{}, ErrInvalidCredentials
	}
	var account Account
	var expected, salt []byte
	err = s.pool.QueryRow(ctx, `SELECT a.player_id,a.username,a.display_name,a.is_admin,(a.suspended_at IS NOT NULL),a.must_change_password,a.password_hash,a.password_salt
		FROM player_accounts a WHERE a.username=$1`, username).
		Scan(&account.PlayerID, &account.Username, &account.DisplayName, &account.IsAdmin, &account.Suspended, &account.MustChangePassword, &expected, &salt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrInvalidCredentials
	}
	if err != nil {
		return Session{}, err
	}
	if account.Suspended {
		return Session{}, ErrSuspended
	}
	actual := hashPassword(password, salt)
	if subtle.ConstantTimeCompare(actual, expected) != 1 {
		return Session{}, ErrInvalidCredentials
	}
	token, err := randomSecret(32)
	if err != nil {
		return Session{}, err
	}
	tokenHash := sha256.Sum256([]byte(token))
	expiresAt := time.Now().UTC().Add(sessionLifetime)
	if _, err := s.pool.Exec(ctx, `INSERT INTO account_sessions(session_hash,player_id,expires_at) VALUES($1,$2,$3)`, tokenHash[:], account.PlayerID, expiresAt); err != nil {
		return Session{}, err
	}
	return Session{Token: token, ExpiresAt: expiresAt, Account: account}, nil
}

func (s *Store) AuthenticateSession(ctx context.Context, token string) (Account, error) {
	if token == "" {
		return Account{}, pgx.ErrNoRows
	}
	tokenHash := sha256.Sum256([]byte(token))
	var account Account
	err := s.pool.QueryRow(ctx, `SELECT a.player_id,a.username,a.display_name,a.is_admin,(a.suspended_at IS NOT NULL),a.must_change_password,
		COALESCE((SELECT season_number FROM world_season WHERE singleton=TRUE),0),
		EXISTS(SELECT 1 FROM season_players p WHERE p.player_id=a.player_id AND p.season_number=(SELECT season_number FROM world_season WHERE singleton=TRUE))
		FROM account_sessions s JOIN player_accounts a ON a.player_id=s.player_id
		WHERE s.session_hash=$1 AND s.expires_at>NOW()`, tokenHash[:]).
		Scan(&account.PlayerID, &account.Username, &account.DisplayName, &account.IsAdmin, &account.Suspended, &account.MustChangePassword, &account.SeasonNumber, &account.JoinedThisSeason)
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, pgx.ErrNoRows
	}
	if err != nil {
		return Account{}, err
	}
	if account.Suspended {
		return Account{}, ErrSuspended
	}
	return account, nil
}

func (s *Store) Logout(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	tokenHash := sha256.Sum256([]byte(token))
	_, err := s.pool.Exec(ctx, `DELETE FROM account_sessions WHERE session_hash=$1`, tokenHash[:])
	return err
}

func (s *Store) ChangePassword(ctx context.Context, playerID, currentPassword, newPassword string) error {
	newHash, newSalt, err := passwordMaterial(newPassword)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var currentHash, currentSalt []byte
	if err := tx.QueryRow(ctx, `SELECT password_hash,password_salt FROM player_accounts WHERE player_id=$1 FOR UPDATE`, playerID).Scan(&currentHash, &currentSalt); err != nil {
		return err
	}
	if currentPassword != "" {
		actual := hashPassword(currentPassword, currentSalt)
		if subtle.ConstantTimeCompare(actual, currentHash) != 1 {
			return ErrInvalidCredentials
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE player_accounts SET password_hash=$2,password_salt=$3,must_change_password=FALSE WHERE player_id=$1`, playerID, newHash, newSalt); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM account_sessions WHERE player_id=$1`, playerID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) UpdateDisplayName(ctx context.Context, playerID, displayName string) (Account, error) {
	displayName, err := NormalizeDisplayName(displayName)
	if err != nil {
		return Account{}, err
	}
	_, err = s.pool.Exec(ctx, `UPDATE player_accounts SET display_name=$2 WHERE player_id=$1`, playerID, displayName)
	if err != nil {
		return Account{}, accountConstraintError(err)
	}
	return s.GetAccount(ctx, playerID)
}

func (s *Store) GetAccount(ctx context.Context, playerID string) (Account, error) {
	var account Account
	err := s.pool.QueryRow(ctx, `SELECT a.player_id,a.username,a.display_name,a.is_admin,(a.suspended_at IS NOT NULL),a.must_change_password,
		COALESCE((SELECT season_number FROM world_season WHERE singleton=TRUE),0),
		EXISTS(SELECT 1 FROM season_players p WHERE p.player_id=a.player_id AND p.season_number=(SELECT season_number FROM world_season WHERE singleton=TRUE))
		FROM player_accounts a WHERE a.player_id=$1`, playerID).
		Scan(&account.PlayerID, &account.Username, &account.DisplayName, &account.IsAdmin, &account.Suspended, &account.MustChangePassword, &account.SeasonNumber, &account.JoinedThisSeason)
	return account, err
}

func (s *Store) CreateAPIToken(ctx context.Context, playerID, name string) (APIToken, error) {
	name, err := ValidateTokenName(name)
	if err != nil {
		return APIToken{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return APIToken{}, err
	}
	defer tx.Rollback(ctx)
	var lockedPlayerID string
	if err := tx.QueryRow(ctx, `SELECT player_id FROM player_accounts WHERE player_id=$1 FOR UPDATE`, playerID).Scan(&lockedPlayerID); err != nil {
		return APIToken{}, err
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM api_tokens WHERE player_id=$1`, playerID).Scan(&count); err != nil {
		return APIToken{}, err
	}
	if count >= maxAPITokens {
		return APIToken{}, ErrTooManyTokens
	}
	token, err := randomSecret(32)
	if err != nil {
		return APIToken{}, err
	}
	id, err := newPlayerID()
	if err != nil {
		return APIToken{}, err
	}
	hash := sha256.Sum256([]byte(token))
	var created time.Time
	err = tx.QueryRow(ctx, `INSERT INTO api_tokens(token_id,token_hash,player_id,name) VALUES($1,$2,$3,$4) RETURNING created_at`, id, hash[:], playerID, name).Scan(&created)
	if err != nil {
		return APIToken{}, accountConstraintError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return APIToken{}, err
	}
	return APIToken{ID: id, Name: name, CreatedAt: created, Token: token}, nil
}

func (s *Store) ListAPITokens(ctx context.Context, playerID string) ([]APIToken, error) {
	rows, err := s.pool.Query(ctx, `SELECT token_id,name,created_at FROM api_tokens WHERE player_id=$1 ORDER BY created_at DESC`, playerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]APIToken, 0)
	for rows.Next() {
		var item APIToken
		if err := rows.Scan(&item.ID, &item.Name, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) RevokeAPIToken(ctx context.Context, playerID, tokenID string) error {
	command, err := s.pool.Exec(ctx, `DELETE FROM api_tokens WHERE player_id=$1 AND token_id=$2`, playerID, tokenID)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (s *Store) ListAdminAccounts(ctx context.Context) ([]AdminAccount, error) {
	rows, err := s.pool.Query(ctx, `SELECT player_id,username,display_name,(suspended_at IS NOT NULL),is_admin FROM player_accounts ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]AdminAccount, 0)
	for rows.Next() {
		var item AdminAccount
		if err := rows.Scan(&item.PlayerID, &item.Username, &item.DisplayName, &item.Suspended, &item.IsAdmin); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ResetPassword(ctx context.Context, playerID string) (string, error) {
	password, err := randomSecret(18)
	if err != nil {
		return "", err
	}
	hash, salt, err := passwordMaterial(password)
	if err != nil {
		return "", err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	result, err := tx.Exec(ctx, `UPDATE player_accounts SET password_hash=$2,password_salt=$3,must_change_password=TRUE WHERE player_id=$1`, playerID, hash, salt)
	if err != nil {
		return "", err
	}
	if result.RowsAffected() == 0 {
		return "", pgx.ErrNoRows
	}
	if _, err := tx.Exec(ctx, `DELETE FROM account_sessions WHERE player_id=$1`, playerID); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return password, nil
}

func (s *Store) SetSuspended(ctx context.Context, playerID string, suspended bool) error {
	var result pgconn.CommandTag
	var err error
	if suspended {
		result, err = s.pool.Exec(ctx, `UPDATE player_accounts SET suspended_at=NOW() WHERE player_id=$1`, playerID)
	} else {
		result, err = s.pool.Exec(ctx, `UPDATE player_accounts SET suspended_at=NULL WHERE player_id=$1`, playerID)
	}
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	if suspended {
		_, err = s.pool.Exec(ctx, `DELETE FROM account_sessions WHERE player_id=$1`, playerID)
	}
	return err
}

func (s *Store) CreateAdmin(ctx context.Context, username, password, displayName string) (Account, error) {
	var count int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM player_accounts WHERE is_admin=TRUE`).Scan(&count); err != nil {
		return Account{}, err
	}
	if count > 0 {
		return Account{}, ErrAdminExists
	}
	return s.CreateAccount(ctx, username, password, displayName, true)
}

// EnsureInitialAdmin creates a one-time bootstrap admin only when the account table is empty.
// The returned password is generated per database and must be changed at first sign-in.
func (s *Store) EnsureInitialAdmin(ctx context.Context) (password string, created bool, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(1297491282)`); err != nil {
		return "", false, err
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM player_accounts`).Scan(&count); err != nil {
		return "", false, err
	}
	if count > 0 {
		if err := tx.Commit(ctx); err != nil {
			return "", false, err
		}
		return "", false, nil
	}
	password, err = randomSecret(18)
	if err != nil {
		return "", false, err
	}
	hash, salt, err := passwordMaterial(password)
	if err != nil {
		return "", false, err
	}
	playerID, err := newPlayerID()
	if err != nil {
		return "", false, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO players(player_id) VALUES($1)`, playerID); err != nil {
		return "", false, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO player_accounts(player_id,username,password_hash,password_salt,display_name,is_admin,must_change_password) VALUES($1,'admin',$2,$3,'Administrator',TRUE,TRUE)`, playerID, hash, salt); err != nil {
		return "", false, accountConstraintError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", false, err
	}
	return password, true, nil
}

func (s *Store) ResetAdminPassword(ctx context.Context, username string) (string, error) {
	username, err := NormalizeUsername(username)
	if err != nil {
		return "", ErrInvalidAccount
	}
	var playerID string
	if err := s.pool.QueryRow(ctx, `SELECT player_id FROM player_accounts WHERE username=$1 AND is_admin=TRUE`, username).Scan(&playerID); err != nil {
		return "", err
	}
	return s.ResetPassword(ctx, playerID)
}

func newPlayerID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}
