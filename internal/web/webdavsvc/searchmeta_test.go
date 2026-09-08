package webdavsvc_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/carddav"

	"example.com/segments/internal/model"
	"example.com/segments/internal/store"
)

// A card without FN must get one derived from N, and search_meta must carry the
// frozen shape so the fn/tel_norm indexes are populated and phone search works.
func TestSearchMetaShape(t *testing.T) {
	ts, pool := newTestServer(t)
	defer ts.Close()
	defer pool.Close()

	hc := webdav.HTTPClientWithBasicAuth(ts.Client(), testUsername, testPassword)
	client, err := carddav.NewClient(hc, ts.URL)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	books, err := client.FindAddressBooks(ctx, "/carddav/"+testUsername+"/contacts/")
	if err != nil {
		t.Fatalf("find books: %v", err)
	}

	card := vcard.Card{}
	card.SetValue(vcard.FieldVersion, "3.0")
	card.SetValue(vcard.FieldUID, "urn:uuid:meta-1")
	card.SetValue(vcard.FieldName, "Doe;John")
	card.SetValue(vcard.FieldEmail, "john@example.com")
	card.SetValue(vcard.FieldTelephone, "+1 (555) 0100")
	if _, err := client.PutAddressObject(ctx, books[0].Path+"john.vcf", card); err != nil {
		t.Fatalf("put: %v", err)
	}

	var vcardText string
	var metaJSON []byte
	if err := pool.QueryRow(ctx, `
		SELECT c.vcard_text, c.search_meta FROM contacts c
		 JOIN users u ON u.id = c.user_id
		WHERE u.username = $1 AND c.filename = 'john.vcf'`,
		testUsername).Scan(&vcardText, &metaJSON); err != nil {
		t.Fatalf("read contact: %v", err)
	}
	if !strings.Contains(vcardText, "FN:John Doe") {
		t.Fatalf("stored vcard missing derived FN: %q", vcardText)
	}

	var meta map[string]any
	if err := json.Unmarshal(metaJSON, &meta); err != nil {
		t.Fatalf("search_meta: %v", err)
	}
	if meta["fn"] != "John Doe" {
		t.Fatalf("fn = %v, want John Doe", meta["fn"])
	}
	norms, _ := meta["tel_norm"].([]any)
	if len(norms) != 1 || norms[0] != "15550100" {
		t.Fatalf("tel_norm = %v, want [15550100]", meta["tel_norm"])
	}

	// Phone search matches digits; name search matches the derived FN.
	uid, err := store.NewUsers(pool).ByLogin(ctx, testUsername)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	actor := model.Actor{UserID: uid.ID, Username: testUsername}
	for _, q := range []string{"5550100", "John"} {
		var found int
		err := store.WithTx(ctx, pool, actor, func(s store.ScopedStore) error {
			res, err := s.Search(ctx, store.SearchParams{Query: q})
			found = len(res.Contacts)
			return err
		})
		if err != nil {
			t.Fatalf("search %q: %v", q, err)
		}
		if found != 1 {
			t.Fatalf("search %q matched %d, want 1", q, found)
		}
	}
}
