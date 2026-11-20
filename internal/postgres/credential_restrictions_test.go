package postgres

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestCredentialRestrictionsUpgrade(t *testing.T) {
	raw := os.Getenv("YAK_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set YAK_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	root, err := pgx.Connect(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close(ctx)
	schema := fmt.Sprintf("cred_upgrade_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := root.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer root.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	dbURL := u.String()
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `CREATE TABLE schema_migrations(version int PRIMARY KEY, name text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	// Reproduce the applied schema without editing its migration files.
	for _, m := range ms {
		if m.Version > 3 {
			break
		}
		if _, err := conn.Exec(ctx, m.SQL); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, `INSERT INTO schema_migrations(version,name) VALUES($1,$2)`, m.Version, m.Name); err != nil {
			t.Fatal(err)
		}
	}
	hash := sha256.Sum256([]byte("fixture-secret"))
	if _, err := conn.Exec(ctx, `INSERT INTO actors(id,kind,handle,display_name) VALUES('act_legacy','human','legacy','Legacy')`); err != nil {
		t.Fatal(err)
	}
	exp := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
	if _, err := conn.Exec(ctx, `INSERT INTO credentials(id,actor_id,label,scopes,verifier,expires_at) VALUES('cred_legacy','act_legacy','legacy','{read}',$1,$2)`, hash[:], exp); err != nil {
		t.Fatal(err)
	}
	applied, err := Migrate(ctx, dbURL)
	if err != nil || len(applied) != len(ms)-3 || applied[0] != "0004_credential_restrictions.sql" {
		t.Fatalf("upgrade = %v, %v", applied, err)
	}
	var manage, delegate bool
	var afterHash []byte
	var afterExp time.Time
	if err := conn.QueryRow(ctx, `SELECT may_manage_credentials,may_delegate,verifier,expires_at FROM credentials WHERE id='cred_legacy'`).Scan(&manage, &delegate, &afterHash, &afterExp); err != nil {
		t.Fatal(err)
	}
	if !manage || !delegate || string(afterHash) != string(hash[:]) || !afterExp.Equal(exp) {
		t.Fatal("migration changed existing credential authority, verifier or expiry")
	}
	// Old inserts get true defaults. An explicit restriction survives a rerun.
	if _, err := conn.Exec(ctx, `INSERT INTO credentials(id,actor_id,label,scopes,verifier,expires_at) VALUES('cred_new','act_legacy','new','{read}',$1,$2)`, hash[:], exp); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, `SELECT may_manage_credentials AND may_delegate FROM credentials WHERE id='cred_new'`).Scan(&manage); err != nil || !manage {
		t.Fatalf("new insert defaults = %v, %v", manage, err)
	}
	if _, err := conn.Exec(ctx, `UPDATE credentials SET may_delegate=false WHERE id='cred_legacy'`); err != nil {
		t.Fatal(err)
	}
	if applied, err := Migrate(ctx, dbURL); err != nil || len(applied) != 0 {
		t.Fatalf("repeat migrate = %v, %v", applied, err)
	}
	if err := conn.QueryRow(ctx, `SELECT may_delegate FROM credentials WHERE id='cred_legacy'`).Scan(&delegate); err != nil || delegate {
		t.Fatalf("restriction after rerun = %v, %v", delegate, err)
	}
}
