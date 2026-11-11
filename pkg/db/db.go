// Package db opens the shared Postgres pool, applies the schema and seeds
// defaults, and wraps LISTEN/NOTIFY for change propagation.
package db

import (
	"context"
	_ "embed"
	"encoding/json"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schema string

// Notification channels.
const (
	ChanPolicy   = "abx_policy"   // payload: device id or "*"
	ChanCommands = "abx_commands" // payload: device id
	ChanConsole  = "abx_console"  // payload: topic
	ChanBundle   = "abx_bundle"   // payload: device id (new bundle stored)
)

func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	var pool *pgxpool.Pool
	var err error
	for i := 0; i < 90; i++ {
		pool, err = pgxpool.New(ctx, url)
		if err == nil {
			if err = pool.Ping(ctx); err == nil {
				return pool, nil
			}
			pool.Close()
		}
		log.Printf("waiting for postgres: %v", err)
		time.Sleep(2 * time.Second)
	}
	return nil, err
}

func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, schema)
	return err
}

// WaitSchema blocks until another service has applied the schema.
func WaitSchema(ctx context.Context, pool *pgxpool.Pool) {
	for {
		var ok bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass('public.correlator_state') IS NOT NULL`).Scan(&ok); err == nil && ok {
			return
		}
		log.Printf("waiting for schema")
		time.Sleep(2 * time.Second)
	}
}

func Notify(ctx context.Context, pool *pgxpool.Pool, channel, payload string) {
	if _, err := pool.Exec(ctx, `SELECT pg_notify($1, $2)`, channel, payload); err != nil {
		log.Printf("notify %s: %v", channel, err)
	}
}

// Listen calls fn for every notification on the channels, reconnecting on
// failure. It blocks until ctx is done.
func Listen(ctx context.Context, pool *pgxpool.Pool, fn func(channel, payload string), channels ...string) {
	for ctx.Err() == nil {
		conn, err := pool.Acquire(ctx)
		if err != nil {
			time.Sleep(time.Second)
			continue
		}
		for _, c := range channels {
			if _, err = conn.Exec(ctx, "LISTEN "+pgx.Identifier{c}.Sanitize()); err != nil {
				break
			}
		}
		for err == nil {
			var n *pgconnNotification
			n, err = waitNotification(ctx, conn)
			if err == nil {
				fn(n.Channel, n.Payload)
			}
		}
		conn.Release()
		if ctx.Err() == nil {
			log.Printf("listen: %v; reconnecting", err)
			time.Sleep(time.Second)
		}
	}
}

type pgconnNotification struct{ Channel, Payload string }

func waitNotification(ctx context.Context, conn *pgxpool.Conn) (*pgconnNotification, error) {
	n, err := conn.Conn().WaitForNotification(ctx)
	if err != nil {
		return nil, err
	}
	return &pgconnNotification{Channel: n.Channel, Payload: n.Payload}, nil
}

// GetSetting decodes settings[key] into v; returns false if absent.
func GetSetting(ctx context.Context, pool *pgxpool.Pool, key string, v any) bool {
	var raw []byte
	if err := pool.QueryRow(ctx, `SELECT value FROM settings WHERE key=$1`, key).Scan(&raw); err != nil {
		return false
	}
	return json.Unmarshal(raw, v) == nil
}

func PutSetting(ctx context.Context, pool *pgxpool.Pool, key string, v any, by string) error {
	b, _ := json.Marshal(v)
	_, err := pool.Exec(ctx, `INSERT INTO settings(key,value,updated_by) VALUES($1,$2,$3)
		ON CONFLICT(key) DO UPDATE SET value=$2, updated_at=now(), updated_by=$3`, key, b, by)
	return err
}
