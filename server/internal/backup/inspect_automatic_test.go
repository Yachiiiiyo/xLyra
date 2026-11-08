package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"slices"
	"testing"

	"github.com/minio/minio-go/v7"

	"xlyra/server/internal/config"
)

func TestAutomaticServiceInspectFile(t *testing.T) {
	t.Parallel()

	confFile := backupReadyAutomaticConfigFile(t)
	encrypted := inspectTestEncryptedArchive(t, []inspectTestArchiveEntry{
		inspectTestManifestEntry(t, v4Manifest([]string{sectionSettings, sectionGatewaySites}), zip.Deflate),
	})

	svc := NewAutomaticService(Service{confFile: confFile}, "master-key")
	svc.WithObjectHeadFetcher(func(ctx context.Context, cfg config.AutomaticBackupConfig, client *minio.Client, key string, length int64) (io.ReadCloser, error) {
		if key != "prod/xlyra-backup-20260621-030000.zip.xlyra" {
			return nil, nil
		}
		if length != inspectObjectHeadBytes {
			return nil, nil
		}
		return io.NopCloser(bytes.NewReader(encrypted)), nil
	})

	result, err := svc.InspectFile(context.Background(), "prod/xlyra-backup-20260621-030000.zip.xlyra")
	if err != nil {
		t.Fatalf("InspectFile: %v", err)
	}
	if result.Legacy {
		t.Fatal("v4 backup should not be legacy")
	}
	if !slices.Equal(result.Sections, []string{sectionSettings, sectionGatewaySites}) {
		t.Fatalf("sections = %#v", result.Sections)
	}
	if len(result.Impact) == 0 {
		t.Fatal("impact should not be empty for settings+gateway_sites backup")
	}

	// 键不在配置的前缀下。
	if _, err := svc.InspectFile(context.Background(), "other/xlyra-backup-20260621-030000.zip.xlyra"); err == nil {
		t.Fatal("key outside prefix should fail")
	}
	// 键为空。
	if _, err := svc.InspectFile(context.Background(), "  "); err == nil {
		t.Fatal("empty key should fail")
	}
}

func TestAutomaticServiceInspectFileRequiresPassphrase(t *testing.T) {
	t.Parallel()

	// 存储配置完整但没保存备份口令:预览应在取口令一步报错。
	cfg := automaticS3TestConfig()
	cfg.Storage.SecretKeyEncrypted = "encrypted"
	cfg.Storage.SecretKeyMasked = "****"
	confFile := automaticConfigFile(t, cfg)
	svc := NewAutomaticService(Service{confFile: confFile}, "master-key")

	_, err := svc.InspectFile(context.Background(), "prod/xlyra-backup-20260621-030000.zip.xlyra")
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("passphrase is required")) {
		t.Fatalf("InspectFile without passphrase err = %v, want backup passphrase is required", err)
	}
}
