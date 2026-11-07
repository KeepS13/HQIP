package pgstore

import (
	"database/sql/driver"
	"strings"
	"testing"
)

func TestValuesNeverBecomeSQLAndLiteralsAreUntouched(t *testing.T) {
	attack := "x'); DROP TABLE portal_usage; --"
	q, err := translate(`INSERT INTO audit(at,action,subject) VALUES(?, 'a.username=? COLLATE NOCASE g.end start,end', ?)`, []driver.NamedValue{{Ordinal: 1, Value: int64(1)}, {Ordinal: 2, Value: attack}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(q, attack) || !strings.Contains(q, "$2::text") || !strings.Contains(q, "'a.username=? COLLATE NOCASE g.end start,end'") {
		t.Fatal(q)
	}
	q, err = translate(`SELECT '?' AS value, 'can''t?' AS escaped`, nil)
	if err != nil || !strings.Contains(q, `'can''t?'`) {
		t.Fatal(q, err)
	}
}

func TestUnsupportedOrUnboundQueriesFailClosed(t *testing.T) {
	for _, q := range []string{"SELECT ?", "SELECT 'broken", "SELECT json_extract(config,'$.nested.path')", "SELECT max(1,2"} {
		if _, err := translate(q, nil); err == nil {
			t.Fatal("unsupported query accepted", q)
		}
	}
	if _, err := translate("SELECT 1", []driver.NamedValue{{Ordinal: 1, Value: "secret"}}); err == nil {
		t.Fatal("extra parameters accepted")
	}
}

func TestPostgresRejectsUnverifiedRemoteConnections(t *testing.T) {
	for _, dsn := range []string{"postgres://user:secret@db.example/hqip?sslmode=disable", "postgres://user:secret@db.example/hqip?sslmode=require", "postgres://user:secret@db.example/hqip?sslmode=prefer", "not a postgres config"} {
		if _, err := Config(dsn); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatal("unsafe transport/error", err)
		}
	}
}

func FuzzSQLParameterBinding(f *testing.F) {
	f.Add("'); DROP TABLE admin; --")
	f.Add("?, $, 'json_set(x)' \x00 中文")
	f.Fuzz(func(t *testing.T, value string) {
		q, err := translate("SELECT ?", []driver.NamedValue{{Ordinal: 1, Value: value}})
		if err != nil || q != "SELECT $1::text" {
			t.Fatal("parameter changed SQL", q, err)
		}
	})
}
