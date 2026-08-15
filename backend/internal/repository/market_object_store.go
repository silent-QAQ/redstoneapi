package repository

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"

	"github.com/silent-QAQ/redstoneapi/internal/config"
	"github.com/silent-QAQ/redstoneapi/internal/redstone/market"
)

const (
	// Marketplace storage limits are expressed in plaintext delivery bytes.
	// AES-GCM prepends a 12-byte nonce and appends a 16-byte authentication tag,
	// so the private ciphertext limit must reserve that fixed overhead.
	defaultMarketObjectBytes       int64 = 32 << 20
	marketEnvelopeOverheadBytes    int64 = 12 + 16
	maxMarketObjectCiphertextBytes int64 = 1<<63 - 1
)

// s3MarketObjectStore stores only envelope ciphertext. Unlike image storage,
// it exposes no public URLs or presigning API.
type s3MarketObjectStore struct {
	client             *s3.Client
	bucket             string
	prefix             string
	maxCiphertextBytes int64
}

var _ market.PrivateObjectStore = (*s3MarketObjectStore)(nil)
var _ market.PrivateObjectStoreHealthChecker = (*s3MarketObjectStore)(nil)

// NewMarketDeliveryResolver constructs the production resolver only when the
// dedicated private marketplace storage configuration is complete. Returning a
// nil resolver for a disabled feature keeps unconfigured deployments fail
// closed at delivery time instead of accidentally falling back to image URLs.
func NewMarketDeliveryResolver(cfg *config.Config) (market.DeliveryContentResolver, error) {
	if cfg == nil {
		return nil, nil
	}
	if !cfg.MarketplaceStorage.Active() {
		return newPreviewMarketDeliveryResolver(cfg)
	}
	storageCfg := cfg.MarketplaceStorage
	fmt.Printf("[DEBUG] storageCfg.EncryptionKey=%q (len=%d)\n", storageCfg.EncryptionKey, len(storageCfg.EncryptionKey))
	client, err := newS3Client(context.Background(), s3ClientParams{
		Endpoint:        storageCfg.Endpoint,
		Region:          storageCfg.Region,
		AccessKeyID:     storageCfg.AccessKeyID,
		SecretAccessKey: storageCfg.SecretAccessKey,
		ForcePathStyle:  storageCfg.ForcePathStyle,
	})
	if err != nil {
		return nil, fmt.Errorf("create marketplace object client: %w", err)
	}
	cipher, err := market.NewEnvelopeCipherFromBase64(storageCfg.EncryptionKeyVersion, storageCfg.EncryptionKey)
	if err != nil {
		return nil, err
	}
	previous := make(map[string]*market.EnvelopeCipher, len(storageCfg.PreviousEncryptionKeys))
	for version, encodedKey := range storageCfg.PreviousEncryptionKeys {
		version = strings.TrimSpace(version)
		if version == "" || version == cipher.KeyVersion() {
			return nil, fmt.Errorf("marketplace previous encryption key version is invalid")
		}
		legacy, err := market.NewEnvelopeCipherFromBase64(version, encodedKey)
		if err != nil {
			return nil, fmt.Errorf("create marketplace previous envelope key: %w", err)
		}
		previous[version] = legacy
	}
	resolver, err := market.NewEncryptedDeliveryResolverWithPreviousKeys(&s3MarketObjectStore{
		client:             client,
		bucket:             storageCfg.Bucket,
		prefix:             normalizeMarketObjectPrefix(storageCfg.Prefix),
		maxCiphertextBytes: normalizedMarketCiphertextLimit(storageCfg.MaxObjectBytes),
	}, cipher, previous)
	if err != nil {
		return nil, err
	}
	return resolver, nil
}

func newPreviewMarketDeliveryResolver(cfg *config.Config) (market.DeliveryContentResolver, error) {
	if !market.PreviewInfrastructureEnabled() {
		return nil, nil
	}
	root := strings.TrimSpace(os.Getenv("REDSTONE_MARKETPLACE_PREVIEW_DIR"))
	if root == "" {
		return nil, fmt.Errorf("preview marketplace object directory is not configured")
	}
	storageCfg := cfg.MarketplaceStorage
	fmt.Printf("[DEBUG Preview] storageCfg.EncryptionKey=%q (len=%d)\n", storageCfg.EncryptionKey, len(storageCfg.EncryptionKey))
	cipher, err := market.NewEnvelopeCipherFromBase64(storageCfg.EncryptionKeyVersion, storageCfg.EncryptionKey)
	if err != nil {
		return nil, err
	}
	return market.NewEncryptedDeliveryResolver(&localMarketObjectStore{
		root:               root,
		prefix:             normalizeMarketObjectPrefix(storageCfg.Prefix),
		maxCiphertextBytes: normalizedMarketCiphertextLimit(storageCfg.MaxObjectBytes),
	}, cipher)
}

// localMarketObjectStore is a preview-only private ciphertext store. It is not
// selected unless REDSTONE_MARKETPLACE_PREVIEW is explicitly enabled.
type localMarketObjectStore struct {
	root               string
	prefix             string
	maxCiphertextBytes int64
}

var _ market.PrivateObjectStore = (*localMarketObjectStore)(nil)
var _ market.PrivateObjectStoreHealthChecker = (*localMarketObjectStore)(nil)

func (s *localMarketObjectStore) HealthCheck(ctx context.Context) error {
	return healthCheckMarketObjectStore(ctx, s, s.prefix)
}

func (s *localMarketObjectStore) Put(ctx context.Context, key string, ciphertext []byte, _ string) error {
	filePath, err := s.pathFor(key)
	if err != nil {
		return err
	}
	if int64(len(ciphertext)) > s.maxCiphertextBytes {
		return fmt.Errorf("market ciphertext exceeds configured object limit")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
		return fmt.Errorf("create preview marketplace object directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(filePath), ".market-cipher-*")
	if err != nil {
		return fmt.Errorf("create preview marketplace object: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("protect preview marketplace object: %w", err)
	}
	if _, err := tmp.Write(ciphertext); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write preview marketplace object: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync preview marketplace object: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close preview marketplace object: %w", err)
	}
	if err := os.Rename(tmpName, filePath); err != nil {
		return fmt.Errorf("commit preview marketplace object: %w", err)
	}
	return nil
}

func (s *localMarketObjectStore) Get(ctx context.Context, key string) ([]byte, error) {
	filePath, err := s.pathFor(key)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("load preview marketplace ciphertext: %w", err)
	}
	if int64(len(data)) > s.maxCiphertextBytes {
		return nil, fmt.Errorf("market ciphertext exceeds configured object limit")
	}
	return data, nil
}

func (s *localMarketObjectStore) Delete(ctx context.Context, key string) error {
	filePath, err := s.pathFor(key)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete preview marketplace ciphertext: %w", err)
	}
	return nil
}

func (s *localMarketObjectStore) pathFor(key string) (string, error) {
	if s == nil || strings.TrimSpace(s.root) == "" {
		return "", market.ErrPrivateObjectStoreNil
	}
	key = strings.TrimSpace(key)
	if key == "" || !strings.HasPrefix(key, s.prefix) || path.Clean(key) != key || strings.Contains(key, "..") || strings.ContainsAny(key, `\\:`) {
		return "", market.ErrPrivateObjectNotFound
	}
	root, err := filepath.Abs(s.root)
	if err != nil {
		return "", err
	}
	filePath, err := filepath.Abs(filepath.Join(root, filepath.FromSlash(key)))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, filePath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", market.ErrPrivateObjectNotFound
	}
	return filePath, nil
}

func (s *s3MarketObjectStore) Put(ctx context.Context, key string, ciphertext []byte, contentType string) error {
	if err := s.validateKey(key); err != nil {
		return err
	}
	if int64(len(ciphertext)) > s.maxCiphertextBytes {
		return fmt.Errorf("market ciphertext exceeds configured object limit")
	}
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      &s.bucket,
		Key:         &key,
		Body:        bytes.NewReader(ciphertext),
		ContentType: &contentType,
	})
	if err != nil {
		return fmt.Errorf("store marketplace ciphertext: %w", err)
	}
	return nil
}

func (s *s3MarketObjectStore) Get(ctx context.Context, key string) ([]byte, error) {
	if err := s.validateKey(key); err != nil {
		return nil, err
	}
	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: &key})
	if err != nil {
		return nil, fmt.Errorf("load marketplace ciphertext: %w", err)
	}
	defer result.Body.Close()
	data, err := io.ReadAll(io.LimitReader(result.Body, s.maxCiphertextBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read marketplace ciphertext: %w", err)
	}
	if int64(len(data)) > s.maxCiphertextBytes {
		return nil, fmt.Errorf("market ciphertext exceeds configured object limit")
	}
	return data, nil
}

func (s *s3MarketObjectStore) Delete(ctx context.Context, key string) error {
	if err := s.validateKey(key); err != nil {
		return err
	}
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &s.bucket, Key: &key})
	if err != nil {
		return fmt.Errorf("delete marketplace ciphertext: %w", err)
	}
	return nil
}

func (s *s3MarketObjectStore) HealthCheck(ctx context.Context) error {
	if s == nil {
		return market.ErrPrivateObjectStoreNil
	}
	return healthCheckMarketObjectStore(ctx, s, s.prefix)
}

func healthCheckMarketObjectStore(ctx context.Context, store market.PrivateObjectStore, prefix string) error {
	probe := make([]byte, 32)
	if _, err := rand.Read(probe); err != nil {
		return fmt.Errorf("generate marketplace storage health probe: %w", err)
	}
	key := normalizeMarketObjectPrefix(prefix) + ".runtime-health/" + uuid.NewString()
	if err := store.Put(ctx, key, probe, "application/octet-stream"); err != nil {
		return fmt.Errorf("write marketplace storage health probe: %w", err)
	}
	loaded, err := store.Get(ctx, key)
	if err != nil {
		_ = store.Delete(context.WithoutCancel(ctx), key)
		return fmt.Errorf("read marketplace storage health probe: %w", err)
	}
	if !bytes.Equal(loaded, probe) {
		_ = store.Delete(context.WithoutCancel(ctx), key)
		return errors.New("marketplace storage health probe integrity check failed")
	}
	if err := store.Delete(ctx, key); err != nil {
		return fmt.Errorf("delete marketplace storage health probe: %w", err)
	}
	return nil
}

func (s *s3MarketObjectStore) validateKey(key string) error {
	if s == nil || s.client == nil || strings.TrimSpace(s.bucket) == "" {
		return market.ErrPrivateObjectStoreNil
	}
	key = strings.TrimSpace(key)
	if key == "" || !strings.HasPrefix(key, s.prefix) || path.Clean(key) != key || strings.Contains(key, "..") {
		return market.ErrPrivateObjectNotFound
	}
	return nil
}

func normalizeMarketObjectPrefix(prefix string) string {
	prefix = strings.TrimSpace(strings.Trim(prefix, "/"))
	if prefix == "" {
		return "redstone-market/"
	}
	return prefix + "/"
}

func normalizedMarketCiphertextLimit(limit int64) int64 {
	if limit <= 0 {
		limit = defaultMarketObjectBytes
	}
	if limit > maxMarketObjectCiphertextBytes-marketEnvelopeOverheadBytes {
		return maxMarketObjectCiphertextBytes
	}
	return limit + marketEnvelopeOverheadBytes
}



