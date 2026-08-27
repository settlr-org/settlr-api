package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nabinkhanal00/settlr-api/internal/auth"
)

func runSeed(ctx context.Context, pool *pgxpool.Pool) error {
	slog.Info("seeding database")

	// Create demo users
	users := []struct {
		ID       uuid.UUID
		Name     string
		Email    string
		Password string
	}{
		{uuid.MustParse("00000000-0000-0000-0000-000000000001"), "Demo User", "demo@settlr.local", "demo-password"},
		{uuid.MustParse("00000000-0000-0000-0000-000000000002"), "Alice", "alice@example.com", "password123"},
		{uuid.MustParse("00000000-0000-0000-0000-000000000003"), "Bob", "bob@example.com", "password123"},
		{uuid.MustParse("00000000-0000-0000-0000-000000000004"), "Charlie", "charlie@example.com", "password123"},
	}
	for _, u := range users {
		hash, _ := auth.HashPassword(u.Password)
		_, err := pool.Exec(ctx,
			`INSERT INTO users (id, name, email, password_hash, email_verified_at) VALUES ($1,$2,$3,$4, now())
			 ON CONFLICT DO NOTHING`, u.ID, u.Name, u.Email, hash)
		if err != nil {
			return fmt.Errorf("seed user %s: %w", u.Email, err)
		}
	}
	// Groups
	group1 := uuid.MustParse("10000000-0000-0000-0000-000000000001")
	group2 := uuid.MustParse("10000000-0000-0000-0000-000000000002")
	_, _ = pool.Exec(ctx,
		`INSERT INTO groups (id, name, description, currency, created_by) VALUES ($1,'Trip to Pokhara','Adventure in Nepal','NPR',$2) ON CONFLICT DO NOTHING`,
		group1, users[0].ID)
	_, _ = pool.Exec(ctx,
		`INSERT INTO groups (id, name, description, currency, created_by) VALUES ($1,'Apartment','Shared apartment expenses','EUR',$2) ON CONFLICT DO NOTHING`,
		group2, users[1].ID)

	for _, gid := range []uuid.UUID{group1, group2} {
		for _, u := range users {
			role := "MEMBER"
			if u.ID == users[0].ID && gid == group1 {
				role = "OWNER"
			}
			if u.ID == users[1].ID && gid == group2 {
				role = "OWNER"
			}
			_, _ = pool.Exec(ctx, `INSERT INTO group_members (group_id, user_id, role) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, gid, u.ID, role)
		}
	}

	// Expenses for group1
	expenses := []struct {
		desc   string
		amount int64
		paidBy uuid.UUID
		splits []uuid.UUID
	}{
		{"Dinner", 12000, users[1].ID, []uuid.UUID{users[1].ID, users[2].ID, users[3].ID}},
		{"Hotel", 30000, users[0].ID, []uuid.UUID{users[0].ID, users[1].ID, users[2].ID, users[3].ID}},
		{"Taxi", 4500, users[2].ID, []uuid.UUID{users[0].ID, users[2].ID}},
		{"Groceries", 8000, users[3].ID, []uuid.UUID{users[0].ID, users[1].ID, users[3].ID}},
		{"Drinks", 6000, users[1].ID, []uuid.UUID{users[1].ID, users[2].ID}},
	}
	for _, e := range expenses {
		eid := uuid.New()
		_, err := pool.Exec(ctx,
			`INSERT INTO expenses (id, group_id, description, amount, currency, split_mode, paid_by, expense_date, created_by)
			 VALUES ($1,$2,$3,$4,'NPR','EQUAL',$5, now(), $5) ON CONFLICT DO NOTHING`,
			eid, group1, e.desc, e.amount, e.paidBy)
		if err != nil {
			continue
		}
		// Equal split
		n := len(e.splits)
		base := e.amount / int64(n)
		rem := e.amount % int64(n)
		for i, pid := range e.splits {
			amt := base
			if int64(i) < rem {
				amt++
			}
			_, _ = pool.Exec(ctx, `INSERT INTO expense_splits (expense_id, user_id, amount) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, eid, pid, amt)
		}
	}

	// Settlements
	sid1 := uuid.New()
	_, _ = pool.Exec(ctx,
		`INSERT INTO settlements (id, group_id, from_user, to_user, amount, currency, note, created_by) VALUES ($1,$2,$3,$4,5000,'NPR','Partial settlement',$5) ON CONFLICT DO NOTHING`,
		sid1, group1, users[2].ID, users[0].ID, users[2].ID)
	sid2 := uuid.New()
	_, _ = pool.Exec(ctx,
		`INSERT INTO settlements (id, group_id, from_user, to_user, amount, currency, created_by) VALUES ($1,$2,$3,$4,3000,'NPR',$5) ON CONFLICT DO NOTHING`,
		sid2, group1, users[3].ID, users[1].ID, users[3].ID)

	slog.Info("seed complete", "users", len(users), "groups", 2, "expenses", len(expenses))
	return nil
}
