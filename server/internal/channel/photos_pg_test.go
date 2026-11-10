package channel

import (
	"context"
	"testing"
	"time"
)

// The photo rows against a real Postgres, from the migration.
func TestPGPhotoLifecycle(t *testing.T) {
	db := testDB(t)
	s := &PGStore{DB: db}
	ctx := context.Background()
	id, err := s.UpsertIdentity(ctx, IMessage, "+15551230000")
	if err != nil {
		t.Fatal(err)
	}
	var taskID string
	if err := db.QueryRow(`INSERT INTO tasks DEFAULT VALUES RETURNING id::text`).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	p := Photo{IdentityID: id.ID, Channel: IMessage, ProviderMessageID: "pm-1", ObjectKey: "inbound/" + id.ID + "/a.jpg", ContentType: "image/jpeg", SizeBytes: 1234}
	a, err := s.SavePhoto(ctx, p)
	if err != nil || a.ID == "" || a.IdentityID != id.ID || a.SizeBytes != 1234 {
		t.Fatalf("save: %+v %v", a, err)
	}
	// A webhook retry of the same message stores nothing twice.
	again, err := s.SavePhoto(ctx, Photo{IdentityID: id.ID, Channel: IMessage, ProviderMessageID: "pm-1", ObjectKey: "inbound/" + id.ID + "/other.jpg", ContentType: "image/jpeg", SizeBytes: 1})
	if err != nil || again.ID != a.ID || again.ObjectKey != a.ObjectKey {
		t.Fatalf("retry: %+v %v", again, err)
	}
	// Only images, only our prefix (the table's own checks).
	if _, err := s.SavePhoto(ctx, Photo{IdentityID: id.ID, Channel: IMessage, ProviderMessageID: "pm-x", ObjectKey: "inbound/x.pdf", ContentType: "application/pdf", SizeBytes: 1}); err == nil {
		t.Fatal("a pdf row was accepted")
	}
	if _, err := s.SavePhoto(ctx, Photo{IdentityID: id.ID, Channel: IMessage, ProviderMessageID: "pm-y", ObjectKey: "https://cdn.example/x.jpg", ContentType: "image/jpeg", SizeBytes: 1}); err == nil {
		t.Fatal("a URL was accepted as the object key")
	}

	since := time.Now().Add(-LoosePhotoWindow)
	loose, err := s.LoosePhotos(ctx, id.ID, since)
	if err != nil || len(loose) != 1 {
		t.Fatalf("loose: %v %v", loose, err)
	}
	claimed, err := s.ClaimLoosePhotos(ctx, id.ID, since)
	if err != nil || len(claimed) != 1 || claimed[0].ID != a.ID {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	if again, _ := s.ClaimLoosePhotos(ctx, id.ID, since); len(again) != 0 {
		t.Fatal("claimed twice")
	}
	if err := s.AttachPhotos(ctx, []string{a.ID}, taskID); err != nil {
		t.Fatal(err)
	}
	on, err := s.TaskPhotos(ctx, taskID)
	if err != nil || len(on) != 1 || on[0].TaskID != taskID {
		t.Fatalf("task photos %v %v", on, err)
	}
	byMsg, err := s.MessagePhotos(ctx, IMessage, []string{"pm-1", `odd"id\`})
	if err != nil || len(byMsg["pm-1"]) != 1 {
		t.Fatalf("message photos %v %v", byMsg, err)
	}

	// Retention: on a task, kept however old; never booked, gone after
	// UnattachedPhotoRetention.
	b, err := s.SavePhoto(ctx, Photo{IdentityID: id.ID, Channel: IMessage, ProviderMessageID: "pm-2", ObjectKey: "inbound/" + id.ID + "/b.png", ContentType: "image/png", SizeBytes: 9})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE channel_photos SET created_at = now() - interval '8 days'`); err != nil {
		t.Fatal(err)
	}
	due, err := s.PhotosToDelete(ctx, time.Now().Add(-UnattachedPhotoRetention), 100)
	if err != nil || len(due) != 1 || due[0].ID != b.ID {
		t.Fatalf("due %v %v", due, err)
	}

	// Account deletion: every photo of the user's numbers is marked, and the
	// identity's purge leaves the rows naming nobody.
	var userID string
	if err := db.QueryRow(`INSERT INTO users DEFAULT VALUES RETURNING id::text`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE channel_identities SET user_id = $1::uuid, verified_at = now() WHERE id = $2::uuid`, userID, id.ID); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	n, err := RequestPhotoDeletionTx(ctx, tx, userID)
	if err != nil || n != 2 {
		t.Fatalf("request deletion: %d %v", n, err)
	}
	if _, err := PurgeUserTx(ctx, tx, userID); err != nil {
		t.Fatalf("purge with photos present: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	due, err = s.PhotosToDelete(ctx, time.Now().Add(-UnattachedPhotoRetention), 100)
	if err != nil || len(due) != 2 {
		t.Fatalf("after account deletion: %v %v", due, err)
	}
	for _, p := range due {
		if p.IdentityID != "" {
			t.Fatalf("row still names the number: %+v", p)
		}
	}
	if on, _ := s.TaskPhotos(ctx, taskID); len(on) != 0 {
		t.Fatal("a photo marked for deletion is still served on the task")
	}
	if err := s.DeletePhotoRows(ctx, []string{due[0].ID, due[1].ID}); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := db.QueryRow(`SELECT count(*) FROM channel_photos`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("left %d %v", left, err)
	}
}
