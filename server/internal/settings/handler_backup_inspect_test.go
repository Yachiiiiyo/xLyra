package settings

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"

	"xlyra/server/internal/backup"
	"xlyra/server/internal/config"
	"xlyra/server/internal/store"
)

// settingsBackupTestStore 在开发 PostgreSQL 中建立一次性 schema(与 backup
// 包的 openAgentBackupTestStore 相同的做法),连不上时跳过。
func settingsBackupTestStore(t *testing.T) *store.Store {
	t.Helper()

	cfg := config.Config{
		AppEnv:           "test",
		DBConnectTimeout: 2 * time.Second,
		DBMinConns:       0,
		DBMaxConns:       4,
		PostgresDSN:      strings.TrimSpace(os.Getenv("POSTGRES_DSN")),
		DBHost:           settingsBackupTestEnv("DB_HOST", "127.0.0.1"),
		DBPort:           5432,
		DBName:           settingsBackupTestEnv("DB_NAME", "xlyra"),
		DBUser:           settingsBackupTestEnv("DB_USER", "postgres"),
		DBPassword:       settingsBackupTestEnv("DB_PASSWORD", "postgres"),
		DBSSLMode:        settingsBackupTestEnv("DB_SSLMODE", "disable"),
	}
	if value := strings.TrimSpace(os.Getenv("DB_PORT")); value != "" {
		port, err := strconv.Atoi(value)
		if err != nil {
			t.Skipf("invalid DB_PORT %q", value)
		}
		cfg.DBPort = port
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	base, err := store.Open(ctx, cfg)
	if err != nil {
		t.Skipf("dev PostgreSQL unavailable: %v", err)
	}
	t.Cleanup(base.Close)

	schema := "xlyra_settings_inspect_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := base.DB().Exec(fmt.Sprintf("CREATE SCHEMA %q", schema)).Error; err != nil {
		t.Skipf("cannot create settings inspect test schema: %v", err)
	}
	t.Cleanup(func() {
		_ = base.DB().Exec(fmt.Sprintf("DROP SCHEMA %q CASCADE", schema)).Error
	})

	parsed, err := url.Parse(cfg.DatabaseDSN())
	if err != nil {
		t.Fatalf("parse test database DSN: %v", err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	cfg.PostgresDSN = parsed.String()
	if err := store.EnsureDatabaseInitialized(ctx, cfg); err != nil {
		t.Fatalf("initialize settings inspect test schema: %v", err)
	}
	st, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("open settings inspect test schema: %v", err)
	}
	t.Cleanup(st.Close)
	return st
}

func settingsBackupTestEnv(key string, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

// settingsInspectTestBackup 导出一份真实的加密备份,返回密文字节。
func settingsInspectTestBackup(t *testing.T, st *store.Store, confFile *config.ConfigFile) []byte {
	t.Helper()

	svc := backup.NewService(st, confFile, settingsTestMasterKey)
	path, _, err := svc.Export(context.Background(), "passphrase")
	if err != nil {
		t.Fatalf("export backup: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	encrypted, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read encrypted backup: %v", err)
	}
	return encrypted
}

func settingsInspectMultipartRequest(t *testing.T, target string, passphrase string, fileContent []byte) *http.Request {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("passphrase", passphrase); err != nil {
		t.Fatalf("write passphrase field: %v", err)
	}
	if fileContent != nil {
		part, err := writer.CreateFormFile("file", "backup.xlyra")
		if err != nil {
			t.Fatalf("create file part: %v", err)
		}
		if _, err := part.Write(fileContent); err != nil {
			t.Fatalf("write file part: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, target, &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func TestInspectBackupHandler(t *testing.T) {
	st := settingsBackupTestStore(t)
	confFile := settingsConfigFile(t)
	h := NewHandlerWithBackup(slog.Default(), confFile, st, settingsTestMasterKey, nil, "", "", nil, nil, nil)
	encrypted := settingsInspectTestBackup(t, st, confFile)

	t.Run("upload head returns inspect result", func(t *testing.T) {
		head := encrypted
		if len(head) > 4<<20 {
			head = head[:4<<20]
		}
		rec := settingsPerform(h.InspectBackup, settingsInspectMultipartRequest(t, "/settings/backup/inspect", "passphrase", head))

		settingsAssertStatus(t, rec, http.StatusOK)
		body := settingsDecodeJSON[struct {
			Backup struct {
				FormatVersion int      `json:"format_version"`
				Legacy        bool     `json:"legacy"`
				Sections      []string `json:"sections"`
				Impact        []struct {
					ID      string `json:"id"`
					Partial bool   `json:"partial"`
				} `json:"impact"`
				Unavailable []struct {
					ID     string `json:"id"`
					Reason string `json:"reason"`
				} `json:"unavailable"`
			} `json:"backup"`
		}](t, rec)
		if body.Backup.Legacy {
			t.Fatal("fresh export should not be legacy")
		}
		if body.Backup.FormatVersion != 4 {
			t.Fatalf("format version = %d, want 4", body.Backup.FormatVersion)
		}
		if len(body.Backup.Sections) == 0 {
			t.Fatal("sections should not be empty")
		}
		// 空内容时应是空数组而不是 null。
		if body.Backup.Impact == nil || body.Backup.Unavailable == nil {
			t.Fatalf("impact/unavailable should be empty arrays, body=%s", rec.Body.String())
		}
	})

	t.Run("missing file part", func(t *testing.T) {
		rec := settingsPerform(h.InspectBackup, settingsInspectMultipartRequest(t, "/settings/backup/inspect", "passphrase", nil))

		assertSettingsErrorCode(t, rec, http.StatusBadRequest, "backup_file_required")
	})

	t.Run("missing passphrase", func(t *testing.T) {
		rec := settingsPerform(h.InspectBackup, settingsInspectMultipartRequest(t, "/settings/backup/inspect", "", encrypted))

		assertSettingsErrorCode(t, rec, http.StatusBadRequest, "backup_passphrase_required")
	})

	t.Run("wrong passphrase", func(t *testing.T) {
		rec := settingsPerform(h.InspectBackup, settingsInspectMultipartRequest(t, "/settings/backup/inspect", "wrong", encrypted))

		assertSettingsErrorCode(t, rec, http.StatusBadRequest, "backup_passphrase_invalid")
	})

	t.Run("body exceeds 8MiB", func(t *testing.T) {
		big := bytes.Repeat([]byte("x"), 9<<20)
		rec := settingsPerform(h.InspectBackup, settingsInspectMultipartRequest(t, "/settings/backup/inspect", "passphrase", big))

		assertSettingsErrorCode(t, rec, http.StatusBadRequest, "invalid_backup_upload")
	})
}

func TestInspectAutomaticBackupFileHandler(t *testing.T) {
	st := settingsBackupTestStore(t)
	confFile := settingsConfigFile(t)
	encrypted := settingsInspectTestBackup(t, st, confFile)

	h := NewHandler(slog.Default(), confFile)
	auto := backup.NewAutomaticService(backup.NewService(st, confFile, settingsTestMasterKey), settingsTestMasterKey)
	if _, err := auto.UpdateConfig(backup.AutomaticConfigInput{
		Cron:             "0 3 * * *",
		RetentionCount:   7,
		Endpoint:         "127.0.0.1:9000",
		Bucket:           "xlyra",
		Prefix:           "prod",
		AccessKey:        "access",
		SecretKey:        "secret",
		BackupPassphrase: "passphrase",
	}); err != nil {
		t.Fatalf("update automatic backup config: %v", err)
	}
	auto.WithObjectHeadFetcher(func(ctx context.Context, cfg config.AutomaticBackupConfig, client *minio.Client, key string, length int64) (io.ReadCloser, error) {
		head := encrypted
		if int64(len(head)) > length {
			head = head[:length]
		}
		return io.NopCloser(bytes.NewReader(head)), nil
	})
	h.autoBackups = auto

	t.Run("returns inspect result", func(t *testing.T) {
		req := settingsJSONRequest(t, http.MethodPost, "/settings/backup/automatic/files/inspect", map[string]string{
			"key": "prod/xlyra-backup-20260621-030000.zip.xlyra",
		})
		rec := settingsPerform(h.InspectAutomaticBackupFile, req)

		settingsAssertStatus(t, rec, http.StatusOK)
		body := settingsDecodeJSON[struct {
			Backup struct {
				Legacy   bool     `json:"legacy"`
				Sections []string `json:"sections"`
			} `json:"backup"`
		}](t, rec)
		if body.Backup.Legacy {
			t.Fatal("fresh export should not be legacy")
		}
		if len(body.Backup.Sections) == 0 {
			t.Fatal("sections should not be empty")
		}
	})

	t.Run("key outside prefix", func(t *testing.T) {
		req := settingsJSONRequest(t, http.MethodPost, "/settings/backup/automatic/files/inspect", map[string]string{
			"key": "other/xlyra-backup-20260621-030000.zip.xlyra",
		})
		rec := settingsPerform(h.InspectAutomaticBackupFile, req)

		settingsAssertStatus(t, rec, http.StatusBadRequest)
	})

	t.Run("invalid json", func(t *testing.T) {
		assertSettingsInvalidJSON(t, h.InspectAutomaticBackupFile, http.MethodPost, "/settings/backup/automatic/files/inspect", `{"key":`)
	})
}

func TestInspectBackupHandlerWithoutAutomaticService(t *testing.T) {
	t.Parallel()

	h := settingsBackupHandler(nil, nil)
	assertSettingsErrorCode(
		t,
		settingsPerform(h.InspectAutomaticBackupFile, settingsRawJSONRequest(http.MethodPost, "/settings/backup/automatic/files/inspect", `{"key":"x"}`)),
		http.StatusServiceUnavailable,
		"backup_unavailable",
	)
}
