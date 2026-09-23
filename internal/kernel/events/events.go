package events

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"syscall"
	"time"

	"github.com/charmbracelet/log"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Event struct {
	ID        uuid.UUID       `json:"id"`
	Type      string          `json:"type"`
	Data      json.RawMessage `json:"data"`
	CreatedAt time.Time       `json:"created_at"`
}

func New(eventType string, resource any) (Event, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return Event{}, err
	}
	data, err := json.Marshal(resource)
	if err != nil {
		return Event{}, fmt.Errorf("events: encode %s: %w", eventType, err)
	}
	return Event{ID: id, Type: eventType, Data: data}, nil
}

const insertSQL = `
	INSERT INTO events (id, type, data)
	SELECT id, type, data::json FROM unnest($1::uuid[], $2::text[], $3::text[]) AS e(id, type, data)`

func columns(evs []Event) ([]uuid.UUID, []string, []string) {
	ids, types, data := make([]uuid.UUID, len(evs)), make([]string, len(evs)), make([]string, len(evs))
	for i, e := range evs {
		ids[i], types[i], data[i] = e.ID, e.Type, string(e.Data)
	}
	return ids, types, data
}

func Queue(b *pgx.Batch, evs ...Event) {
	if len(evs) == 0 {
		return
	}
	ids, types, data := columns(evs)
	b.Queue(insertSQL, ids, types, data)
}

type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func Insert(ctx context.Context, q execer, evs ...Event) error {
	if len(evs) == 0 {
		return nil
	}
	ids, types, data := columns(evs)
	_, err := q.Exec(ctx, insertSQL, ids, types, data)
	return err
}

var (
	ErrNotFound = errors.New("events: not found")
	ErrInvalid  = errors.New("events: invalid input")
)

type Config struct {
	AllowInsecureURLs bool
	PollInterval      time.Duration
	Workers           int
	BatchSize         int
	Timeout           time.Duration
	Lease             time.Duration
	Retries           []time.Duration
	Client            *http.Client
	Retention         time.Duration
	PruneInterval     time.Duration
	PruneBatch        int
}

func (c Config) withDefaults() Config {
	if c.PollInterval <= 0 {
		c.PollInterval = time.Second
	}

	if c.Workers <= 0 {
		c.Workers = 8
	}

	if c.BatchSize <= 0 {
		c.BatchSize = 100
	}

	if c.Timeout <= 0 {
		c.Timeout = 10 * time.Second
	}

	if c.Lease <= 0 {
		c.Lease = time.Minute
	}

	if c.Lease < c.Timeout {
		c.Lease = 2 * c.Timeout
	}

	if c.Retries == nil {

		c.Retries = []time.Duration{
			time.Minute, 5 * time.Minute, 30 * time.Minute, time.Hour, 2 * time.Hour,
			5 * time.Hour, 10 * time.Hour, 10 * time.Hour, 12 * time.Hour, 12 * time.Hour, 12 * time.Hour,
		}
	}

	if c.Retention <= 0 {
		c.Retention = 30 * 24 * time.Hour
	}

	if c.PruneInterval <= 0 {
		c.PruneInterval = time.Hour
	}

	if c.PruneBatch <= 0 {
		c.PruneBatch = 5000
	}

	if c.Client == nil {
		dialer := &net.Dialer{Timeout: 5 * time.Second}
		if !c.AllowInsecureURLs {
			dialer.Control = publicOnly
		}

		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.DialContext = dialer.DialContext
		c.Client = &http.Client{
			Transport: transport,

			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}

	return c
}

func publicOnly(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}

	ip, err := netip.ParseAddr(host)
	if err != nil {
		return err
	}

	ip = ip.Unmap()
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() {
		return fmt.Errorf("events: refusing to deliver to non-public address %s", ip)
	}

	return nil
}

type Service struct {
	pool *pgxpool.Pool
	log  *log.Logger
	cfg  Config
}

func NewService(pool *pgxpool.Pool, logger *log.Logger, cfg Config) *Service {
	return &Service{pool: pool, log: logger.WithPrefix("events"), cfg: cfg.withDefaults()}
}

func (s *Service) Name() string { return "events" }

func (s *Service) Migrations() fs.FS { return Migrations() }

func Migrations() fs.FS {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic(err)
	}
	return sub
}

func (s *Service) Run(ctx context.Context) error {
	s.log.Info("started", "interval", s.cfg.PollInterval, "workers", s.cfg.Workers, "retention", s.cfg.Retention)
	defer s.log.Info("stopped")

	ticker := time.NewTicker(s.cfg.PollInterval)
	defer ticker.Stop()

	prune := time.NewTicker(s.cfg.PruneInterval)
	defer prune.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-prune.C:
			if _, err := s.Prune(ctx); err != nil {
				s.log.Error("prune failed", "err", err)
			}
			continue
		case <-ticker.C:
		}

		for {
			n, err := s.Dispatch(ctx)
			if err != nil {
				s.log.Error("dispatch failed", "err", err)
				break
			}

			if n < s.cfg.BatchSize {
				break
			}
		}

		for {
			n, err := s.Deliver(ctx)
			if err != nil {
				s.log.Error("delivery failed", "err", err)
				break
			}

			if n < s.cfg.BatchSize {
				break
			}
		}
	}
}

func (s *Service) Deliver(ctx context.Context) (int, error) {
	claimed, err := s.claim(ctx)
	if err != nil || len(claimed) == 0 {
		return 0, err
	}

	sem := make(chan struct{}, s.cfg.Workers)
	var wg sync.WaitGroup

	for _, d := range claimed {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			s.send(context.WithoutCancel(ctx), d)
		})
	}

	wg.Wait()

	return len(claimed), nil
}
