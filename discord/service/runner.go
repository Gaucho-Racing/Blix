package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gaucho-racing/blix/discord/client"
	"github.com/gaucho-racing/blix/discord/model"
)

type Runner struct {
	Bridge  *Bridge
	Gateway *client.Discord
	Ready   atomic.Bool
}

func (r *Runner) Run(ctx context.Context) error {
	sqlDB, err := r.Bridge.DB.DB()
	if err != nil {
		return err
	}
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	for {
		var leader bool
		if err = conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock(hashtextextended($1,0))", "blix/discord/"+r.Bridge.Config.GuildID).Scan(&leader); err != nil {
			return fmt.Errorf("acquire Discord leadership: %w", err)
		}
		if leader {
			break
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(5 * time.Second):
		}
	}
	defer conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock(hashtextextended($1,0))", "blix/discord/"+r.Bridge.Config.GuildID)
	if err = r.Bridge.DB.WithContext(ctx).Model(&model.Inbox{}).Where("state = ?", "processing").Updates(map[string]any{"state": "pending", "next_attempt_at": time.Now().UTC()}).Error; err != nil {
		return err
	}
	if err = r.Bridge.DB.WithContext(ctx).Model(&model.Outbox{}).Where("state = ?", "sending").Update("next_attempt_at", time.Now().UTC()).Error; err != nil {
		return err
	}
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	remove := r.Gateway.Session.AddHandler(func(_ *discordgo.Session, message *discordgo.MessageCreate) {
		if err := r.Bridge.Receive(workerCtx, r.Gateway.BotID.Load().(string), message.Message); err != nil {
			r.Bridge.Log.Errorw("persist Discord input", "message_id", message.ID)
		}
	})
	defer remove()
	if err = r.Gateway.Session.Open(); err != nil {
		return errors.New("connect Discord Gateway; check bot credentials and permissions")
	}
	defer r.Gateway.Session.Close()
	r.Ready.Store(true)
	defer r.Ready.Store(false)
	var workers sync.WaitGroup
	workers.Add(2)
	go func() { defer workers.Done(); r.inbox(workerCtx) }()
	go func() { defer workers.Done(); r.delivery(workerCtx) }()
	defer func() { cancel(); workers.Wait() }()
	return r.monitor(ctx, conn)
}
func (r *Runner) monitor(ctx context.Context, conn *sql.Conn) error {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			check, cancel := context.WithTimeout(ctx, 3*time.Second)
			err := conn.PingContext(check)
			cancel()
			if err != nil {
				return errors.New("Discord leadership connection lost")
			}
		}
	}
}
func (r *Runner) inbox(ctx context.Context) {
	ticker := time.NewTicker(r.Bridge.Config.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for i := 0; i < 10; i++ {
				work, err := r.Bridge.ProcessInbox(ctx)
				if err != nil {
					r.Bridge.Log.Warnw("Discord input processing failed", "reason", safeError(err))
				}
				if !work || ctx.Err() != nil {
					break
				}
			}
		}
	}
}
func (r *Runner) delivery(ctx context.Context) {
	ticker := time.NewTicker(r.Bridge.Config.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.Bridge.PollEvents(ctx); err != nil {
				r.Bridge.Log.Warnw("Discord event polling failed", "reason", safeError(err))
			}
			for i := 0; i < 20; i++ {
				work, err := r.Bridge.Deliver(ctx)
				if err != nil {
					r.Bridge.Log.Warnw("Discord reply delivery failed", "reason", safeError(err))
				}
				if !work || ctx.Err() != nil {
					break
				}
			}
		}
	}
}

func safeError(err error) string {
	var upstream *client.Error
	if errors.As(err, &upstream) {
		return upstream.Error()
	}
	return "storage_or_delivery_state_error"
}
