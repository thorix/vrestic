package vault

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hashicorp/vault-client-go"
	"github.com/hashicorp/vault-client-go/schema"
)

const saTokenPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"

const passwordsPath = "vrestic/passwords"

// Vault is the client for HashiCorp Vault
type Vault struct {
	Token      string
	Address    string
	Mount      string
	Role       string
	SecretsDir string
	Client     *vault.Client
	Ctx        context.Context
}

// New sets up defaults from environment variables
func (v *Vault) New() error {
	v.Address = os.Getenv("VAULT_ADDR")
	v.Token = os.Getenv("VAULT_TOKEN")
	v.Role = os.Getenv("VAULT_ROLE")
	if v.Role == "" {
		v.Role = "vrestic"
	}
	v.SecretsDir = os.Getenv("RESTIC_SECRETS_DIR")
	v.Mount = "kv"

	if v.SecretsDir == "" && v.Address == "" {
		return errors.New("missing required environment variable: VAULT_ADDR or RESTIC_SECRETS_DIR")
	}
	return nil
}

// Connect establishes a connection to Vault using either a token or Kubernetes
// auth. Skipped entirely when RESTIC_SECRETS_DIR is set — passwords come from
// mounted files and no Vault connection is needed at runtime.
func (v *Vault) Connect() error {
	if v.SecretsDir != "" {
		slog.Debug("Using file-based secrets, skipping Vault connection", "dir", v.SecretsDir)
		return nil
	}

	v.Ctx = context.Background()
	var err error

	v.Client, err = vault.New(
		vault.WithAddress(v.Address),
		vault.WithRequestTimeout(30*time.Second),
	)
	if err != nil {
		return err
	}

	// Prefer VAULT_TOKEN if set (local development)
	if v.Token != "" {
		slog.Debug("Authenticating with Vault token")
		return v.Client.SetToken(v.Token)
	}

	// Fall back to Kubernetes service account auth (in-cluster)
	slog.Debug("Authenticating with Kubernetes service account")
	jwt, err := os.ReadFile(saTokenPath)
	if err != nil {
		return fmt.Errorf("no VAULT_TOKEN set and cannot read service account token: %w", err)
	}

	loginResp, err := v.Client.Auth.KubernetesLogin(v.Ctx, schema.KubernetesLoginRequest{
		Jwt:  string(jwt),
		Role: v.Role,
	}, vault.WithMountPath("kubernetes"))
	if err != nil {
		return fmt.Errorf("kubernetes auth failed: %w", err)
	}

	return v.Client.SetToken(loginResp.Auth.ClientToken)
}

// ReadPassword reads a password from a mounted secret file (when
// RESTIC_SECRETS_DIR is set) or from the consolidated Vault secret at
// kv/vrestic/passwords where each snapshot name is a key.
func (v *Vault) ReadPassword(name string) (string, error) {
	if len(name) == 0 {
		return "", errors.New("missing given name")
	}

	if v.SecretsDir != "" {
		return v.readPasswordFromFile(name)
	}

	secret, err := v.Client.Secrets.KvV2Read(v.Ctx, passwordsPath, vault.WithMountPath(v.Mount))
	if err != nil {
		return "", fmt.Errorf("reading passwords from Vault: %w", err)
	}

	password, ok := secret.Data.Data[name].(string)
	if !ok || len(password) == 0 {
		return "", fmt.Errorf("no password found for %q in kv/%s", name, passwordsPath)
	}

	slog.Debug("Using password from Vault", "name", name)
	return password, nil
}

// WritePassword adds a password to the consolidated Vault secret at
// kv/vrestic/passwords. Reads existing passwords first to avoid overwriting.
// Not supported when using file-based secrets.
func (v *Vault) WritePassword(name string, password string) error {
	if v.SecretsDir != "" {
		return fmt.Errorf("cannot auto-create password when using file-based secrets: add %q to kv/vrestic/passwords in Vault, then re-sync vrestic-passwords secret", name)
	}

	// Read existing passwords
	data := make(map[string]any)
	secret, err := v.Client.Secrets.KvV2Read(v.Ctx, passwordsPath, vault.WithMountPath(v.Mount))
	if err != nil {
		// If the secret doesn't exist yet, start fresh
		if !strings.Contains(err.Error(), "404") {
			return fmt.Errorf("reading existing passwords: %w", err)
		}
	} else {
		data = secret.Data.Data
	}

	// Add the new password
	data[name] = password

	_, err = v.Client.Secrets.KvV2Write(v.Ctx, passwordsPath, schema.KvV2WriteRequest{
		Data: data,
	}, vault.WithMountPath(v.Mount))
	if err != nil {
		return fmt.Errorf("writing password to Vault: %w", err)
	}

	slog.Info("Password written to Vault", "name", name, "path", "kv/"+passwordsPath)
	return nil
}

func (v *Vault) readPasswordFromFile(name string) (string, error) {
	path := filepath.Join(v.SecretsDir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading password file %s: %w", path, err)
	}
	password := strings.TrimSpace(string(data))
	if len(password) == 0 {
		return "", fmt.Errorf("empty password in file: %s", path)
	}
	slog.Debug("Using password from file", "name", name)
	return password, nil
}

// PasswordExists checks if a password already exists in Vault for the given name.
func (v *Vault) PasswordExists(name string) (bool, error) {
	if v.SecretsDir != "" {
		path := filepath.Join(v.SecretsDir, name)
		_, err := os.Stat(path)
		if err == nil {
			return true, nil
		}
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}

	secret, err := v.Client.Secrets.KvV2Read(v.Ctx, passwordsPath, vault.WithMountPath(v.Mount))
	if err != nil {
		if strings.Contains(err.Error(), "404") {
			return false, nil
		}
		return false, err
	}

	_, ok := secret.Data.Data[name].(string)
	return ok, nil
}

// ReadConfig reads the vrestic config from Vault at kv/vrestic/config.
func (v *Vault) ReadConfig() (string, error) {
	if v.SecretsDir != "" {
		return "", errors.New("ReadConfig not supported in file-based mode")
	}

	secret, err := v.Client.Secrets.KvV2Read(v.Ctx, "vrestic/config", vault.WithMountPath(v.Mount))
	if err != nil {
		return "", fmt.Errorf("reading config from Vault: %w", err)
	}

	content, ok := secret.Data.Data["config.yaml"].(string)
	if !ok || len(content) == 0 {
		return "", errors.New("no config.yaml key found in kv/vrestic/config")
	}

	return content, nil
}

// WriteConfig uploads the vrestic config to Vault at kv/vrestic/config.
func (v *Vault) WriteConfig(yamlContent string) error {
	if v.SecretsDir != "" {
		return errors.New("WriteConfig not supported in file-based mode")
	}

	_, err := v.Client.Secrets.KvV2Write(v.Ctx, "vrestic/config", schema.KvV2WriteRequest{
		Data: map[string]any{
			"config.yaml": yamlContent,
		},
	}, vault.WithMountPath(v.Mount))
	if err != nil {
		return fmt.Errorf("writing config to Vault: %w", err)
	}

	slog.Info("Config written to Vault", "path", "kv/vrestic/config")
	return nil
}

