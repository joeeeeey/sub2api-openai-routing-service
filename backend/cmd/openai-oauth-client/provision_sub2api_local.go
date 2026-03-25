package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/setup"
	"github.com/redis/go-redis/v9"
)

const (
	defaultProvisionDataDir          = ".openai-routing-service"
	defaultProvisionDBHost           = "127.0.0.1"
	defaultProvisionDBPort           = 15432
	defaultProvisionDBUser           = "sub2api"
	defaultProvisionDBPassword       = "sub2api_local_dev"
	defaultProvisionDBName           = "sub2api"
	defaultProvisionRedisHost        = "127.0.0.1"
	defaultProvisionRedisPort        = 16379
	defaultProvisionRedisDB          = 0
	defaultProvisionAdminEmail       = "admin@sub2api.local"
	defaultProvisionAdminPassword    = "sub2api-admin-local"
	defaultProvisionAdminUsername    = "admin"
	defaultProvisionAdminBalance     = 1000000
	defaultProvisionServerHost       = "127.0.0.1"
	defaultProvisionServerPort       = 8080
	defaultProvisionServiceUserEmail = "openai-routing-service@local.invalid"
	defaultProvisionServiceUserName  = "openai-routing-service"
	defaultProvisionGroupName        = "openai-routing-default"
	defaultProvisionAPIKeyName       = "openai-routing-service-key"
	defaultProvisionStaticKey        = "dev-local"
	defaultProvisionAccountPriority  = 10
	defaultProvisionConcurrency      = 50
)

func runProvisionSub2APILocal(ctx context.Context, args []string) error {
	args = normalizeFlagAliases(args)
	fs := flag.NewFlagSet("provision-sub2api-local", flag.ContinueOnError)
	dataDir := fs.String("data-dir", defaultProvisionDataDirPath(), "data dir used by local sub2api setup")
	statePath := fs.String("state-file", defaultStateFile(), "oauth state file")
	dbHost := fs.String("db-host", defaultProvisionDBHost, "postgres host")
	dbPort := fs.Int("db-port", defaultProvisionDBPort, "postgres port")
	dbUser := fs.String("db-user", defaultProvisionDBUser, "postgres user")
	dbPassword := fs.String("db-password", defaultProvisionDBPassword, "postgres password")
	dbName := fs.String("db-name", defaultProvisionDBName, "postgres database")
	redisHost := fs.String("redis-host", defaultProvisionRedisHost, "redis host")
	redisPort := fs.Int("redis-port", defaultProvisionRedisPort, "redis port")
	redisPassword := fs.String("redis-password", "", "redis password")
	redisDB := fs.Int("redis-db", defaultProvisionRedisDB, "redis database")
	adminEmail := fs.String("admin-email", defaultProvisionAdminEmail, "local admin email")
	adminPassword := fs.String("admin-password", defaultProvisionAdminPassword, "local admin password")
	serverHost := fs.String("server-host", defaultProvisionServerHost, "local server host to write into config")
	serverPort := fs.Int("server-port", defaultProvisionServerPort, "local server port to write into config")
	serviceUserEmail := fs.String("service-user-email", defaultProvisionServiceUserEmail, "email of synthetic service user")
	serviceUserName := fs.String("service-user-name", defaultProvisionServiceUserName, "username of synthetic service user")
	groupName := fs.String("group-name", defaultProvisionGroupName, "openai group name used by routing service")
	staticKey := fs.String("static-key", defaultProvisionStaticKey, "external static key used by compat service")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	state, err := loadState(*statePath)
	if err != nil {
		return fmt.Errorf("load oauth state: %w", err)
	}

	dataDirAbs, err := filepath.Abs(*dataDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dataDirAbs, 0o700); err != nil {
		return err
	}
	if err := os.Setenv("DATA_DIR", dataDirAbs); err != nil {
		return err
	}
	devEnvPath := filepath.Join(dataDirAbs, "dev.env")
	totpKey := parseEnvValue(devEnvPath, "TOTP_ENCRYPTION_KEY")
	if totpKey == "" {
		totpKey = randomHex(32)
	}
	if err := os.Setenv("TOTP_ENCRYPTION_KEY", totpKey); err != nil {
		return err
	}

	if setup.NeedsSetup() {
		cfg := &setup.SetupConfig{
			Database: setup.DatabaseConfig{
				Host:     *dbHost,
				Port:     *dbPort,
				User:     *dbUser,
				Password: *dbPassword,
				DBName:   *dbName,
				SSLMode:  "disable",
			},
			Redis: setup.RedisConfig{
				Host:      *redisHost,
				Port:      *redisPort,
				Password:  *redisPassword,
				DB:        *redisDB,
				EnableTLS: false,
			},
			Admin: setup.AdminConfig{
				Email:    *adminEmail,
				Password: *adminPassword,
			},
			Server: setup.ServerConfig{
				Host: *serverHost,
				Port: *serverPort,
				Mode: "debug",
			},
			JWT: setup.JWTConfig{
				Secret:     randomHex(32),
				ExpireHour: 24,
			},
			Timezone: "Asia/Shanghai",
		}
		if err := setup.Install(cfg); err != nil {
			return fmt.Errorf("initial local setup failed: %w", err)
		}
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config from %s: %w", dataDirAbs, err)
	}
	entClient, err := repository.ProvideEnt(cfg)
	if err != nil {
		return err
	}
	defer entClient.Close()
	sqlDB, err := repository.ProvideSQLDB(entClient)
	if err != nil {
		return err
	}
	groupRepo := repository.NewGroupRepository(entClient, sqlDB)
	userRepo := repository.NewUserRepository(entClient, sqlDB)
	apiKeyRepo := repository.NewAPIKeyRepository(entClient, sqlDB)
	accountRepo := repository.NewAccountRepository(entClient, sqlDB, nil)

	adminUser, err := ensureProvisionAdminUser(ctx, userRepo, *adminEmail, *adminPassword)
	if err != nil {
		return err
	}
	group, err := ensureProvisionGroup(ctx, groupRepo, *groupName)
	if err != nil {
		return err
	}
	user, err := ensureProvisionUser(ctx, userRepo, *serviceUserEmail, *serviceUserName)
	if err != nil {
		return err
	}
	account, err := ensureProvisionOpenAIAccount(ctx, accountRepo, group.ID, state)
	if err != nil {
		return err
	}
	serviceAPIKey, err := ensureProvisionAPIKey(ctx, apiKeyRepo, user.ID, group.ID, devEnvPath, defaultProvisionAPIKeyName)
	if err != nil {
		return err
	}
	if err := clearProvisionRedisCaches(ctx, cfg); err != nil {
		return fmt.Errorf("clear local redis caches: %w", err)
	}
	if err := writeProvisionEnvFile(devEnvPath, dataDirAbs, serviceAPIKey, *staticKey, totpKey, *adminEmail, *adminPassword, group.Name); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "Provisioned local sub2api routing resources in %s\n", dataDirAbs)
	fmt.Fprintf(os.Stderr, "admin_user_id=%d admin_user_balance=%.2f\n", adminUser.ID, adminUser.Balance)
	fmt.Fprintf(os.Stderr, "group_id=%d group_name=%s\n", group.ID, group.Name)
	fmt.Fprintf(os.Stderr, "service_user_id=%d service_user_email=%s\n", user.ID, user.Email)
	fmt.Fprintf(os.Stderr, "account_id=%d account_name=%s\n", account.ID, account.Name)
	fmt.Fprintf(os.Stderr, "service_api_key=%s\n", serviceAPIKey)
	fmt.Fprintf(os.Stderr, "static_key=%s\n", *staticKey)
	fmt.Fprintf(os.Stderr, "admin_email=%s\n", *adminEmail)
	fmt.Fprintf(os.Stderr, "admin_password=%s\n", *adminPassword)
	fmt.Fprintf(os.Stderr, "env_file=%s\n", filepath.Join(dataDirAbs, "dev.env"))
	return nil
}

func defaultProvisionDataDirPath() string {
	root := detectProjectRoot()
	return filepath.Join(root, defaultProvisionDataDir)
}

func ensureProvisionAdminUser(ctx context.Context, repo service.UserRepository, email, password string) (*service.User, error) {
	user, err := repo.GetByEmail(ctx, email)
	if err == nil && user != nil {
		updated := false
		if strings.TrimSpace(user.Role) != service.RoleAdmin {
			user.Role = service.RoleAdmin
			updated = true
		}
		if strings.TrimSpace(user.Status) != service.StatusActive {
			user.Status = service.StatusActive
			updated = true
		}
		if strings.TrimSpace(user.Username) == "" {
			user.Username = defaultProvisionAdminUsername
			updated = true
		}
		if user.Balance < defaultProvisionAdminBalance {
			user.Balance = defaultProvisionAdminBalance
			updated = true
		}
		if user.Concurrency < defaultProvisionConcurrency {
			user.Concurrency = defaultProvisionConcurrency
			updated = true
		}
		if updated {
			if err := repo.Update(ctx, user); err != nil {
				return nil, err
			}
		}
		return user, nil
	}
	if !errors.Is(err, service.ErrUserNotFound) {
		return nil, err
	}

	user = &service.User{
		Email:       email,
		Username:    defaultProvisionAdminUsername,
		Role:        service.RoleAdmin,
		Status:      service.StatusActive,
		Balance:     defaultProvisionAdminBalance,
		Concurrency: defaultProvisionConcurrency,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := user.SetPassword(password); err != nil {
		return nil, err
	}
	if err := repo.Create(ctx, user); err != nil {
		return nil, err
	}
	return user, nil
}

func ensureProvisionGroup(ctx context.Context, repo service.GroupRepository, name string) (*service.Group, error) {
	groups, err := repo.ListActiveByPlatform(ctx, service.PlatformOpenAI)
	if err != nil {
		return nil, err
	}
	for i := range groups {
		if strings.EqualFold(strings.TrimSpace(groups[i].Name), strings.TrimSpace(name)) {
			return &groups[i], nil
		}
	}
	group := &service.Group{
		Name:                  name,
		Description:           "Local OpenAI routing service group",
		Platform:              service.PlatformOpenAI,
		RateMultiplier:        1.0,
		IsExclusive:           false,
		Status:                service.StatusActive,
		SubscriptionType:      service.SubscriptionTypeStandard,
		AllowMessagesDispatch: true,
		DefaultMappedModel:    "gpt-5.4",
	}
	if err := repo.Create(ctx, group); err != nil {
		return nil, err
	}
	return group, nil
}

func ensureProvisionUser(ctx context.Context, repo service.UserRepository, email, username string) (*service.User, error) {
	user, err := repo.GetByEmail(ctx, email)
	if err == nil && user != nil {
		return user, nil
	}
	if !errors.Is(err, service.ErrUserNotFound) {
		return nil, err
	}
	user = &service.User{
		Email:       email,
		Username:    username,
		Role:        service.RoleAdmin,
		Status:      service.StatusActive,
		Balance:     1000000,
		Concurrency: defaultProvisionConcurrency,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := user.SetPassword(randomHex(12)); err != nil {
		return nil, err
	}
	if err := repo.Create(ctx, user); err != nil {
		return nil, err
	}
	return user, nil
}

func ensureProvisionOpenAIAccount(ctx context.Context, repo service.AccountRepository, groupID int64, state *savedState) (*service.Account, error) {
	if state == nil {
		return nil, errors.New("missing oauth state")
	}
	params := pagination.PaginationParams{Page: 1, PageSize: 1000}
	accounts, _, err := repo.ListWithFilters(ctx, params, service.PlatformOpenAI, service.AccountTypeOAuth, "", strings.TrimSpace(state.Email), 0, "")
	if err != nil {
		return nil, err
	}
	for i := range accounts {
		acc := accounts[i]
		if acc.GetCredential("chatgpt_account_id") == strings.TrimSpace(state.ChatGPTAccountID) || acc.GetCredential("email") == strings.TrimSpace(state.Email) {
			acc.Credentials = buildProvisionCredentials(state)
			acc.Platform = service.PlatformOpenAI
			acc.Type = service.AccountTypeOAuth
			acc.Status = service.StatusActive
			acc.Schedulable = true
			acc.Concurrency = defaultProvisionConcurrency
			acc.Priority = defaultProvisionAccountPriority
			if err := repo.Update(ctx, &acc); err != nil {
				return nil, err
			}
			if err := repo.BindGroups(ctx, acc.ID, []int64{groupID}); err != nil {
				return nil, err
			}
			return &acc, nil
		}
	}

	account := &service.Account{
		Name:        buildProvisionAccountName(state),
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeOAuth,
		Credentials: buildProvisionCredentials(state),
		Extra:       map[string]any{},
		Concurrency: defaultProvisionConcurrency,
		Priority:    defaultProvisionAccountPriority,
		Status:      service.StatusActive,
		Schedulable: true,
		GroupIDs:    []int64{groupID},
	}
	if err := repo.Create(ctx, account); err != nil {
		return nil, err
	}
	if err := repo.BindGroups(ctx, account.ID, []int64{groupID}); err != nil {
		return nil, err
	}
	return account, nil
}

func ensureProvisionAPIKey(ctx context.Context, repo service.APIKeyRepository, userID, groupID int64, devEnvPath string, keyName string) (string, error) {
	if existingKey := parseEnvValue(devEnvPath, "OPENAI_COMPAT_SERVICE_API_KEY"); existingKey != "" {
		if _, err := repo.GetByKey(ctx, existingKey); err == nil {
			return existingKey, nil
		}
	}

	params := pagination.PaginationParams{Page: 1, PageSize: 1000}
	keys, _, err := repo.ListByUserID(ctx, userID, params, service.APIKeyListFilters{})
	if err != nil {
		return "", err
	}
	for i := range keys {
		if strings.EqualFold(strings.TrimSpace(keys[i].Name), strings.TrimSpace(keyName)) && keys[i].GroupID != nil && *keys[i].GroupID == groupID {
			return keys[i].Key, nil
		}
	}

	keyValue := "sk-routing-" + randomHex(18)
	apiKey := &service.APIKey{
		UserID:  userID,
		Key:     keyValue,
		Name:    keyName,
		GroupID: &groupID,
		Status:  service.StatusActive,
	}
	if err := repo.Create(ctx, apiKey); err != nil {
		return "", err
	}
	return keyValue, nil
}

func buildProvisionCredentials(state *savedState) map[string]any {
	creds := map[string]any{
		"access_token": state.AccessToken,
		"expires_at":   time.Unix(state.ExpiresAtUnix, 0).Format(time.RFC3339),
		"email":        state.Email,
	}
	if strings.TrimSpace(state.RefreshToken) != "" {
		creds["refresh_token"] = state.RefreshToken
	}
	if strings.TrimSpace(state.IDToken) != "" {
		creds["id_token"] = state.IDToken
	}
	if strings.TrimSpace(state.ClientID) != "" {
		creds["client_id"] = state.ClientID
	}
	if strings.TrimSpace(state.ChatGPTAccountID) != "" {
		creds["chatgpt_account_id"] = state.ChatGPTAccountID
	}
	if strings.TrimSpace(state.ChatGPTUserID) != "" {
		creds["chatgpt_user_id"] = state.ChatGPTUserID
	}
	if strings.TrimSpace(state.OrganizationID) != "" {
		creds["organization_id"] = state.OrganizationID
	}
	if strings.TrimSpace(state.PlanType) != "" {
		creds["plan_type"] = state.PlanType
	}
	return creds
}

func buildProvisionAccountName(state *savedState) string {
	if state == nil {
		return "OpenAI Routing OAuth Account"
	}
	if strings.TrimSpace(state.Email) != "" {
		return "OpenAI Routing OAuth - " + strings.TrimSpace(state.Email)
	}
	return "OpenAI Routing OAuth Account"
}

func writeProvisionEnvFile(path string, dataDir string, serviceAPIKey string, staticKey string, totpKey string, adminEmail string, adminPassword string, groupName string) error {
	content := strings.Join([]string{
		"DATA_DIR=" + dataDir,
		"OPENAI_COMPAT_SERVICE_API_KEY=" + serviceAPIKey,
		"OPENAI_COMPAT_STATIC_KEY=" + staticKey,
		"TOTP_ENCRYPTION_KEY=" + totpKey,
		"SUB2API_ADMIN_EMAIL=" + adminEmail,
		"SUB2API_ADMIN_PASSWORD=" + adminPassword,
		"OPENAI_ROUTING_GROUP_NAME=" + groupName,
		"",
	}, "\n")
	return os.WriteFile(path, []byte(content), 0o600)
}

func clearProvisionRedisCaches(ctx context.Context, cfg *config.Config) error {
	if cfg == nil {
		return nil
	}
	client := redis.NewClient(&redis.Options{
		Addr:     cfg.Redis.Address(),
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	})
	defer func() { _ = client.Close() }()

	patterns := []string{
		"apikey:auth:*",
		"billing:balance:*",
	}
	for _, pattern := range patterns {
		if err := deleteRedisKeysByPattern(ctx, client, pattern); err != nil {
			return err
		}
	}
	return nil
}

func deleteRedisKeysByPattern(ctx context.Context, client *redis.Client, pattern string) error {
	var cursor uint64
	for {
		keys, nextCursor, err := client.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return err
		}
		if len(keys) > 0 {
			if err := client.Del(ctx, keys...).Err(); err != nil {
				return err
			}
		}
		cursor = nextCursor
		if cursor == 0 {
			return nil
		}
	}
}

func parseEnvValue(path string, key string) string {
	body, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	prefix := key + "="
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

func randomHex(n int) string {
	if n <= 0 {
		return ""
	}
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}
