package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/pandabase/astrum/internal/kernel/auth"
	"github.com/pandabase/astrum/internal/kernel/config"
	"github.com/pandabase/astrum/internal/kernel/db"
	"github.com/pandabase/astrum/internal/kernel/logger"
	"github.com/pandabase/astrum/internal/kernel/typeid"
)

const keysUsage = `usage:
  astrum keys create -name <name> [-role admin|write|read] [-expires <duration>]
  astrum keys revoke -id <key_...>`

func runKeys(ctx context.Context, cfg config.Config, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(keysUsage)
	}
	base, err := logger.New(os.Stderr, "warn", cfg.LogFormat)
	if err != nil {
		return err
	}
	pool, err := db.Connect(ctx, cfg.DatabaseURL, db.Options{MaxConns: 2, AllowUnsafeDurability: cfg.AllowUnsafeDurability})
	if err != nil {
		return err
	}
	defer pool.Close()
	authn := auth.New(pool, base)
	if err := db.Migrate(ctx, pool, base, authn.Name(), authn.Migrations()); err != nil {
		return err
	}

	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("keys create", flag.ContinueOnError)
		name := fs.String("name", "", "who or what the key is for")
		role := fs.String("role", string(auth.RoleAdmin), "admin, write or read")
		expires := fs.Duration("expires", 0, "lifetime, such as 720h; zero never expires")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() > 0 {
			return fmt.Errorf("keys create: unexpected argument %q", fs.Arg(0))
		}
		if *expires < 0 {
			return errors.New("keys create: -expires must not be negative")
		}
		in := auth.CreateInput{Name: *name, Role: auth.Role(*role)}
		if *expires > 0 {
			at := time.Now().Add(*expires)
			in.ExpiresAt = &at
		}
		k, token, err := authn.Create(ctx, in)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "created %s key %s (%s)\n%s\nStore it now: it cannot be shown again.\n",
			k.Role, typeid.Encode("key", k.ID), k.Name, token)
		return nil
	case "revoke":
		fs := flag.NewFlagSet("keys revoke", flag.ContinueOnError)
		raw := fs.String("id", "", "the key_... id to revoke")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() > 0 {
			return fmt.Errorf("keys revoke: unexpected argument %q", fs.Arg(0))
		}
		id, err := typeid.Parse("key", *raw)
		if err != nil {
			return err
		}
		k, err := authn.Revoke(ctx, id)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "revoked %s (%s)\n", typeid.Encode("key", k.ID), k.Name)
		return nil
	}
	return errors.New(keysUsage)
}
