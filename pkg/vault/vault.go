package vault

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/hashicorp/vault-client-go"
	"github.com/hashicorp/vault-client-go/schema"
)

const saTokenPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"

// Vault is the client for HashiCorp Vault
type Vault struct {
	Token    string
	Address  string
	BasePath string
	Mount    string
	Role     string
	Client   *vault.Client
	Ctx      context.Context
}

// New sets up defaults from environment variables
func (v *Vault) New() error {
	v.Address = os.Getenv("VAULT_ADDR")
	if v.Address == "" {
		return errors.New("missing required environment variable: VAULT_ADDR")
	}
	v.Token = os.Getenv("VAULT_TOKEN")
	v.Role = os.Getenv("VAULT_ROLE")
	if v.Role == "" {
		v.Role = "vrestic"
	}
	v.BasePath = "rbackup"
	v.Mount = "kv"
	return nil
}

// Connect establishes a connection to Vault using either a token or Kubernetes auth
func (v *Vault) Connect() error {
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

// ReadPassword reads a password from Vault at the given path
func (v *Vault) ReadPassword(name string) (string, error) {
	if len(name) == 0 {
		return "", errors.New("missing given name")
	}
	path := sanitizePath(v.BasePath) + "/" + name

	err := v.Connect()
	if err != nil {
		return "", err
	}

	secret, err := v.Client.Secrets.KvV2Read(v.Ctx, path, vault.WithMountPath(v.Mount))
	if err != nil {
		return "", err
	}

	password, ok := secret.Data.Data["password"].(string)
	if !ok || len(password) == 0 {
		return "", fmt.Errorf("invalid Vault password at path: %s", path)
	}

	slog.Debug("Using password from Vault", "name", name)
	return password, nil
}

// WritePassword saves a password to Vault
func (v *Vault) WritePassword(name string, password string) error {
	path := sanitizePath(v.BasePath) + "/" + name

	err := v.Connect()
	if err != nil {
		return err
	}

	_, err = v.Client.Secrets.KvV2Write(v.Ctx, path, schema.KvV2WriteRequest{
		Data: map[string]any{
			"password": password,
		},
	}, vault.WithMountPath(v.Mount))
	if err != nil {
		return err
	}

	slog.Info("Secret written successfully", "path", path)
	return nil
}

func sanitizePath(s string) string {
	return ensureNoTrailingSlash(ensureNoLeadingSlash(strings.TrimSpace(s)))
}

func ensureNoTrailingSlash(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

func ensureNoLeadingSlash(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	for len(s) > 0 && s[0] == '/' {
		s = s[1:]
	}
	return s
}
