package webdavsvc

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"example.com/segments/internal/auth"
	"example.com/segments/internal/resolve"
	"example.com/segments/internal/store"
)

// The go-webdav carddav.Handler serves sync-collection only on its client, not
// its server, and its address-book PROPFIND carries no getctag. This filter
// answers those two on the address-book collection and passes every other
// request to the library handler.
type davFilter struct {
	next http.Handler
	b    *backend
}

func (f *davFilter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	actor, ok := auth.ActorFrom(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	ab := strings.TrimSuffix(addressBookPath(actor.Username), "/")
	if strings.TrimSuffix(r.URL.Path, "/") != ab {
		f.next.ServeHTTP(w, r)
		return
	}
	if r.Method == "PROPFIND" && r.Header.Get("Depth") == "0" {
		handleBookProp(w, r, f.b, actor)
		return
	}
	if r.Method == "REPORT" {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		if bytes.Contains(body, []byte("sync-collection")) {
			handleSync(w, r, f.b, actor, body)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body)) // rewind for the library
	}
	f.next.ServeHTTP(w, r)
}

// getctag and the sync token share one value: the newest change id plus the
// principal's epoch.
func syncToken(ctx context.Context, b *backend, actor auth.Actor) (string, error) {
	maxID, err := store.MaxCommittedChangeID(ctx, b.pool, actor.UserID)
	if err != nil {
		return "", err
	}
	if actor.PrincipalID == nil {
		return fmt.Sprintf("%d-0", maxID), nil
	}
	var epoch int64
	err = store.WithTx(ctx, b.pool, actor, func(s store.ScopedStore) error {
		epoch, err = s.PrincipalEpoch(ctx)
		return err
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d-%d", maxID, epoch), nil
}

func handleBookProp(w http.ResponseWriter, r *http.Request, b *backend, actor auth.Actor) {
	ctag, err := syncToken(r.Context(), b, actor)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	body := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:carddav" xmlns:cs="http://calendarserver.org/ns/">
 <d:response>
  <d:href>%s</d:href>
  <d:propstat>
   <d:prop>
    <d:displayname>Contacts</d:displayname>
    <d:resourcetype><c:addressbook/></d:resourcetype>
    <d:sync-token>%s</d:sync-token>
    <cs:getctag>%s</cs:getctag>
    <d:supported-report-set>
     <d:supported-report><d:report><d:sync-collection/></d:report></d:supported-report>
     <d:supported-report><d:report><c:addressbook-query/></d:report></d:supported-report>
     <d:supported-report><d:report><c:addressbook-multiget/></d:report></d:supported-report>
    </d:supported-report-set>
   </d:prop>
   <d:status>HTTP/1.1 200 OK</d:status>
  </d:propstat>
 </d:response>
</d:multistatus>`, r.URL.Path, ctag, ctag)
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)
	_, _ = w.Write([]byte(body))
}

type syncTokenQuery struct {
	Token string `xml:"DAV: sync-token"`
}

type syncEntry struct {
	filename string
	etag     string // empty for deletions
	deleted  bool
}

func handleSync(w http.ResponseWriter, r *http.Request, b *backend, actor auth.Actor, body []byte) {
	var q syncTokenQuery
	if err := xml.Unmarshal(body, &q); err != nil {
		http.Error(w, "bad sync report", http.StatusBadRequest)
		return
	}

	var entries []syncEntry
	token := ""

	err := store.WithTx(r.Context(), b.pool, actor, func(s store.ScopedStore) error {
		var maxID int64
		if err := s.Tx.QueryRow(r.Context(),
			`SELECT COALESCE(MAX(id),0) FROM contact_changes WHERE user_id = $1`,
			actor.UserID).Scan(&maxID); err != nil {
			return err
		}
		epoch, err := s.PrincipalEpoch(r.Context())
		if err != nil {
			return err
		}

		lastID, clientEpoch, ok := parseSyncToken(q.Token)
		if !ok || clientEpoch != epoch {
			// Unknown token or a tier flip: give the client a fresh token and
			// let it re-list.
			token = fmt.Sprintf("%d-%d", maxID, epoch)
			return nil
		}

		rows, err := s.StreamDeltas(r.Context(), store.StreamOptions{AfterID: lastID, Limit: 500})
		if err != nil {
			return err
		}
		latest := map[string]store.Change{}
		tail := lastID
		for _, c := range rows {
			latest[c.Filename] = c
			tail = c.ID
		}
		visible, err := resolve.Resolver{}.VisibleBooks(r.Context(), s)
		if err != nil {
			return err
		}
		for _, c := range latest {
			if c.ChangeType == "delete" {
				entries = append(entries, syncEntry{filename: c.Filename, deleted: true})
				continue
			}
			contact, err := s.LiveContactByFilename(r.Context(), c.Filename)
			if err != nil {
				continue
			}
			books, err := s.ContactBookIDs(r.Context(), contact.ID)
			if err != nil {
				continue
			}
			if anyVisible(books, visible) {
				entries = append(entries, syncEntry{filename: c.Filename, etag: contact.ETag})
			}
		}
		token = fmt.Sprintf("%d-%d", tail, epoch)
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	ab := addressBookPath(actor.Username)
	var out strings.Builder
	out.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n")
	out.WriteString(`<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:carddav">`)
	for _, e := range entries {
		if e.deleted {
			fmt.Fprintf(&out, `<d:response><d:href>%s%s</d:href><d:status>HTTP/1.1 404 Not Found</d:status></d:response>`, ab, e.filename)
		} else {
			fmt.Fprintf(&out, `<d:response><d:href>%s%s</d:href><d:propstat><d:prop><d:getetag>"%s"</d:getetag></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`, ab, e.filename, e.etag)
		}
	}
	fmt.Fprintf(&out, `<d:sync-token>%s</d:sync-token></d:multistatus>`, token)

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)
	_, _ = w.Write([]byte(out.String()))
}

func parseSyncToken(tok string) (int64, int64, bool) {
	idPart, epochPart, ok := strings.Cut(tok, "-")
	if !ok {
		return 0, 0, false
	}
	lastID, err1 := strconv.ParseInt(idPart, 10, 64)
	epoch, err2 := strconv.ParseInt(epochPart, 10, 64)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return lastID, epoch, true
}
