// Command sharecodex-server is the central server. `serve` runs the API;
// `admin` manages people, invites, accounts, shares and devices directly
// in the database.
//
// Configuration comes from the environment:
//
//	DATABASE_URL  Postgres connection string (required)
//	LISTEN_ADDR   address to listen on (default :8080)
//	PUBLIC_URL    base URL members reach the server at, used in join links
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/server/httpapi"
	"github.com/KoukeNeko/ShareCodex/internal/server/storage"
	"github.com/KoukeNeko/ShareCodex/internal/syncapi"
)

const usage = `usage:
  sharecodex-server serve
  sharecodex-server admin person add --name NAME [--admin]
  sharecodex-server admin person list
  sharecodex-server admin invite --person PERSON [--ttl 24h]
  sharecodex-server admin account list
  sharecodex-server admin account label --account ACCOUNT --label LABEL
  sharecodex-server admin share list --account ACCOUNT
  sharecodex-server admin share set --account ACCOUNT --person PERSON --weight W
  sharecodex-server admin device list
  sharecodex-server admin device revoke --id DEVICE

PERSON and ACCOUNT accept an ID or an exact name/label.
A share weight of 0 removes a member from the allotment but keeps their usage.`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "sharecodex-server:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return errors.New("DATABASE_URL is not set")
	}
	store, err := storage.Open(ctx, url)
	if err != nil {
		return err
	}
	defer store.Close()

	switch args[0] {
	case "serve":
		return serve(ctx, store)
	case "admin":
		return admin(ctx, store, args[1:])
	default:
		return errors.New(usage)
	}
}

func serve(ctx context.Context, store *storage.Store) error {
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	srv := &http.Server{
		Addr:              addr,
		Handler:           httpapi.New(store, log),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()
	log.Info("listening", "addr", addr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func admin(ctx context.Context, store *storage.Store, args []string) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	cmd := args[0]
	if len(args) > 1 && !strings.HasPrefix(args[1], "-") {
		cmd += " " + args[1]
		args = args[1:]
	}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	name := fs.String("name", "", "display name")
	isAdmin := fs.Bool("admin", false, "grant admin")
	personRef := fs.String("person", "", "person ID or name")
	accountRef := fs.String("account", "", "account ID or label")
	label := fs.String("label", "", "account label")
	weight := fs.Float64("weight", -1, "share weight")
	ttl := fs.Duration("ttl", 24*time.Hour, "invite lifetime")
	id := fs.String("id", "", "device ID")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	defer tw.Flush()

	switch cmd {
	case "person add":
		if *name == "" {
			return errors.New("--name is required")
		}
		p, err := store.AddPerson(ctx, *name, *isAdmin)
		if err != nil {
			return err
		}
		fmt.Fprintf(tw, "Added %s (%s)\n", p.DisplayName, p.ID)

	case "person list":
		persons, err := store.Persons(ctx)
		if err != nil {
			return err
		}
		fmt.Fprintln(tw, "ID\tNAME\tADMIN")
		for _, p := range persons {
			fmt.Fprintf(tw, "%s\t%s\t%v\n", p.ID, p.DisplayName, p.IsAdmin)
		}

	case "invite":
		p, err := store.FindPerson(ctx, *personRef)
		if err != nil {
			return err
		}
		base := strings.TrimRight(os.Getenv("PUBLIC_URL"), "/")
		if base == "" {
			return errors.New("PUBLIC_URL is not set; join links need the address members reach the server at")
		}
		code, expires, err := store.CreateInvite(ctx, p.ID, *ttl)
		if err != nil {
			return err
		}
		fmt.Fprintf(tw, "Join link for %s (single use, expires %s):\n%s%s%s\n",
			p.DisplayName, expires.Local().Format(time.DateTime), base, syncapi.PathJoin, code)

	case "account list":
		accounts, err := store.Accounts(ctx)
		if err != nil {
			return err
		}
		fmt.Fprintln(tw, "ID\tPROVIDER\tLABEL\tPLAN\tHINT")
		for _, a := range accounts {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", a.ID, a.Provider, a.Label, a.PlanType, a.Hint)
		}

	case "account label":
		a, err := store.FindAccount(ctx, *accountRef)
		if err != nil {
			return err
		}
		if strings.TrimSpace(*label) == "" {
			return errors.New("--label is required")
		}
		return store.SetAccountLabel(ctx, a.ID, strings.TrimSpace(*label))

	case "share list":
		a, err := store.FindAccount(ctx, *accountRef)
		if err != nil {
			return err
		}
		members, err := store.Members(ctx, a.ID)
		if err != nil {
			return err
		}
		var total float64
		for _, m := range members {
			total += m.ShareWeight
		}
		fmt.Fprintln(tw, "PERSON\tWEIGHT\tALLOTTED")
		for _, m := range members {
			allotted := 0.0
			if total > 0 {
				allotted = m.ShareWeight / total * 100
			}
			fmt.Fprintf(tw, "%s\t%g\t%.1f%%\n", m.Name, m.ShareWeight, allotted)
		}

	case "share set":
		if *weight < 0 {
			return errors.New("--weight must be 0 or more")
		}
		a, err := store.FindAccount(ctx, *accountRef)
		if err != nil {
			return err
		}
		p, err := store.FindPerson(ctx, *personRef)
		if err != nil {
			return err
		}
		return store.SetShareWeight(ctx, a.ID, p.ID, *weight)

	case "device list":
		devices, err := store.Devices(ctx)
		if err != nil {
			return err
		}
		fmt.Fprintln(tw, "ID\tPERSON\tNAME\tPLATFORM\tLAST SEEN\tSTATUS")
		for _, d := range devices {
			seen, status := "-", "active"
			if d.LastSeenAt != nil {
				seen = d.LastSeenAt.Local().Format(time.DateTime)
			}
			if d.RevokedAt != nil {
				status = "revoked"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", d.ID, d.PersonName, d.Name, d.Platform, seen, status)
		}

	case "device revoke":
		if *id == "" {
			return errors.New("--id is required")
		}
		return store.RevokeDevice(ctx, *id)

	default:
		return errors.New(usage)
	}
	return nil
}
