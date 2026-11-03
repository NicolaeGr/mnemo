package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nicolaegr/mnemo/internal/model"
	"github.com/nicolaegr/mnemo/internal/store"
)

func TestAcceptInviteBuildsNetwork(t *testing.T) {
	pool := resetPool(t)
	ctx := context.Background()
	users := store.NewUsers(pool)

	alice, err := users.Signup(ctx, "alice", "alice@example.com", "Alice", "pw", 4)
	if err != nil {
		t.Fatalf("signup alice: %v", err)
	}
	bob, err := users.Signup(ctx, "bob", "bob@example.com", "Bob", "pw", 4)
	if err != nil {
		t.Fatalf("signup bob: %v", err)
	}

	inv, err := store.CreateInvite(ctx, pool, alice.ID, store.InviteNetwork, 0, "Bob@Example.com")
	if err != nil {
		t.Fatalf("create invite: %v", err)
	}
	if inv.Token == "" || inv.Status != "pending" || inv.InviterName != "Alice" {
		t.Fatalf("invite = %+v", inv)
	}

	pending, err := store.PendingInvites(ctx, pool, bob.ID, bob.Email)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != inv.ID {
		t.Fatalf("pending = %+v, want the new invite", pending)
	}

	accepted, err := store.AcceptInvite(ctx, pool, inv.Token, bob.ID)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if accepted.Status != "accepted" || accepted.InviteeID == nil || *accepted.InviteeID != bob.ID {
		t.Fatalf("accepted = %+v", accepted)
	}

	for _, pair := range [][2]int64{{alice.ID, bob.ID}, {bob.ID, alice.ID}} {
		ok, err := store.NetworkHas(ctx, pool, pair[0], pair[1])
		if err != nil {
			t.Fatalf("network has: %v", err)
		}
		if !ok {
			t.Fatalf("network %d -> %d missing", pair[0], pair[1])
		}
	}
	members, err := store.NetworkMembers(ctx, pool, alice.ID)
	if err != nil {
		t.Fatalf("members: %v", err)
	}
	if len(members) != 1 || members[0].ID != bob.ID {
		t.Fatalf("members = %+v", members)
	}

	// Accepting twice keeps a single network entry.
	if _, err := store.AcceptInvite(ctx, pool, inv.Token, bob.ID); err != nil {
		t.Fatalf("re-accept: %v", err)
	}
	members, err = store.NetworkMembers(ctx, pool, alice.ID)
	if err != nil {
		t.Fatalf("members after re-accept: %v", err)
	}
	if len(members) != 1 {
		t.Fatalf("members after re-accept = %+v, want one", members)
	}

	// The inviter cannot accept, and nobody else can take a bound invite.
	selfInvite, err := store.CreateInvite(ctx, pool, alice.ID, store.InviteNetwork, 0, bob.Email)
	if err != nil {
		t.Fatalf("create self invite: %v", err)
	}
	if _, err := store.AcceptInvite(ctx, pool, selfInvite.Token, alice.ID); !errors.Is(err, model.ErrConflict) {
		t.Fatalf("inviter accept err = %v, want ErrConflict", err)
	}
	carol, err := users.Signup(ctx, "carol", "carol@example.com", "Carol", "pw", 4)
	if err != nil {
		t.Fatalf("signup carol: %v", err)
	}
	if _, err := store.AcceptInvite(ctx, pool, inv.Token, carol.ID); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("foreign accept err = %v, want ErrNotFound", err)
	}
}

func TestDeclineInvite(t *testing.T) {
	pool := resetPool(t)
	ctx := context.Background()
	users := store.NewUsers(pool)

	alice, err := users.Signup(ctx, "adecline", "adecline@example.com", "Alice", "pw", 4)
	if err != nil {
		t.Fatalf("signup alice: %v", err)
	}
	bob, err := users.Signup(ctx, "bdecline", "bdecline@example.com", "Bob", "pw", 4)
	if err != nil {
		t.Fatalf("signup bob: %v", err)
	}
	inv, err := store.CreateInvite(ctx, pool, alice.ID, store.InviteNetwork, 0, bob.Email)
	if err != nil {
		t.Fatalf("create invite: %v", err)
	}
	if err := store.DeclineInvite(ctx, pool, inv.Token, bob.ID); err != nil {
		t.Fatalf("decline: %v", err)
	}
	stored, err := store.InviteByToken(ctx, pool, inv.Token)
	if err != nil {
		t.Fatalf("read invite: %v", err)
	}
	if stored.Status != "revoked" {
		t.Fatalf("status = %q, want revoked", stored.Status)
	}
	ok, err := store.NetworkHas(ctx, pool, alice.ID, bob.ID)
	if err != nil {
		t.Fatalf("network has: %v", err)
	}
	if ok {
		t.Fatal("declined invite added a network member")
	}
	if _, err := store.AcceptInvite(ctx, pool, inv.Token, bob.ID); !errors.Is(err, model.ErrConflict) {
		t.Fatalf("accept revoked err = %v, want ErrConflict", err)
	}
}

func TestInviteByTokenUnknown(t *testing.T) {
	pool := resetPool(t)
	if _, err := store.InviteByToken(context.Background(), pool, "nope"); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
