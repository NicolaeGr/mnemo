package admin

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/a-h/templ"

	"github.com/nicolaegr/mnemo/internal/model"
	"github.com/nicolaegr/mnemo/internal/store"
	vcardmeta "github.com/nicolaegr/mnemo/internal/vcard"
	"github.com/nicolaegr/mnemo/internal/web/pages"
)

// importCard is one parsed card ready to write: the canonical body, its UID and
// search index, and the list names its CATEGORIES asked for.
type importCard struct {
	filename   string
	uid        string
	text       string
	meta       []byte
	categories []string
}

func (a *Admin) exportModal(r *http.Request) (string, templ.Component) {
	d := pages.ExportData{}
	var book *store.Book
	if raw := r.URL.Query().Get("book"); raw != "" {
		b, err := a.getBook(r, raw)
		if err != nil {
			return "Export contacts", pages.ErrorBanner(err.Error())
		}
		book = &b
		d.BookID, d.BookName = b.ID, b.DisplayName
	}
	ids, err := parseIDList(r.URL.Query()["id"])
	if err != nil {
		return "Export contacts", pages.ErrorBanner("The contact selection was malformed.")
	}
	d.IDs = ids

	var bookID *int64
	if book != nil {
		bookID = &book.ID
	}
	names, err := a.exportNames(r, bookID, ids)
	if err != nil {
		return "Export contacts", pages.ErrorBanner(err.Error())
	}
	d.Single = len(names) == 1
	d.BaseName = presetExportName(book, names)

	switch {
	case len(ids) > 0:
		d.Summary = countNoun(len(ids), "selected contact", "selected contacts")
	case d.BookName != "":
		d.Summary = "All contacts in " + d.BookName + "."
	default:
		d.Summary = "All contacts."
	}
	return "Export contacts", pages.ExportModal(d)
}

func (a *Admin) exportNames(r *http.Request, bookID *int64, ids []int64) ([]string, error) {
	var names []string
	err := store.WithTx(r.Context(), a.pool, a.actor(r), func(s store.ScopedStore) error {
		var e error
		names, e = s.ExportNames(r.Context(), bookID, ids, 4)
		return e
	})
	return names, err
}

func (a *Admin) importModal(r *http.Request) (string, templ.Component) {
	return "Import contacts", pages.ImportModal()
}

// exportContacts writes the chosen contacts as one vCard file or a zip of one
// file per contact. Category rewriting happens on the way out only; stored rows
// never change.
func (a *Admin) exportContacts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	var book *store.Book
	if raw := q.Get("book"); raw != "" {
		b, err := a.getBook(r, raw)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		book = &b
	}
	var bookID *int64
	if book != nil {
		bookID = &book.ID
	}

	ids, err := parseIDList(q["id"])
	if err != nil {
		http.Error(w, "bad contact selection", http.StatusBadRequest)
		return
	}

	var contacts []store.ExportContact
	err = store.WithTx(r.Context(), a.pool, a.actor(r), func(s store.ScopedStore) error {
		var e error
		contacts, e = s.ExportContacts(r.Context(), bookID, ids)
		return e
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	categoryMode := q.Get("categories")
	for i := range contacts {
		switch categoryMode {
		case "exclude":
			contacts[i].VCardText = vcardmeta.WithCategories(contacts[i].VCardText, nil)
		case "only":
			if book != nil {
				keep := make([]string, 0, 1)
				for _, name := range contacts[i].BookNames {
					if name == book.DisplayName {
						keep = append(keep, name)
					}
				}
				contacts[i].VCardText = vcardmeta.WithCategories(contacts[i].VCardText, keep)
			}
		}
	}

	base := safeExportName(q.Get("name"))
	if q.Get("format") == "zip" && len(contacts) != 1 {
		writeContactsZip(w, base, contacts)
		return
	}
	writeContactsVCF(w, base, contacts)
}

func writeContactsVCF(w http.ResponseWriter, base string, contacts []store.ExportContact) {
	w.Header().Set("Content-Type", "text/vcard; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.vcf"`, base))
	for _, c := range contacts {
		_, _ = io.WriteString(w, c.VCardText)
		if !strings.HasSuffix(c.VCardText, "\n") {
			_, _ = io.WriteString(w, "\n")
		}
	}
}

func writeContactsZip(w http.ResponseWriter, base string, contacts []store.ExportContact) {
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.zip"`, base))
	zw := zip.NewWriter(w)
	for _, c := range contacts {
		f, err := zw.Create(c.Filename)
		if err != nil {
			return
		}
		if _, err := io.WriteString(f, c.VCardText); err != nil {
			return
		}
	}
	_ = zw.Close()
}

// importContacts parses a .vcf or a zip of .vcf files and writes every card in
// one transaction. CATEGORIES names resolve to lists, which are created approved
// for a user-driven import. Nothing else in the file changes stored tags.
func (a *Admin) importContacts(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "could not read the upload", http.StatusBadRequest)
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "choose a .vcf or .zip file", http.StatusBadRequest)
		return
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "could not read the upload", http.StatusBadRequest)
		return
	}
	cards, skipped, err := parseImportFile(data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	imported, created := 0, 0
	err = store.WithTx(ctx, a.pool, a.actor(r), func(s store.ScopedStore) error {
		if err := store.LockSync(ctx, s.Tx, a.actor(r).UserID); err != nil {
			return err
		}
		books := make(map[string]int64)
		for _, card := range cards {
			taken, err := s.LiveUIDTaken(ctx, card.uid)
			if err != nil {
				return err
			}
			if taken {
				skipped++
				continue
			}

			bookIDs := make([]int64, 0, len(card.categories))
			for _, name := range card.categories {
				key := strings.ToLower(strings.TrimSpace(name))
				if key == "" {
					continue
				}
				id, ok := books[key]
				if !ok {
					b, isNew, err := s.EnsureBook(ctx, name, true)
					if errors.Is(err, model.ErrPrecondition) {
						continue
					}
					if err != nil {
						return err
					}
					if isNew {
						created++
					}
					id = b.ID
					books[key] = id
				}
				bookIDs = append(bookIDs, id)
			}

			_, err = s.SaveContact(ctx, store.PutContactParams{
				Filename:   card.filename,
				UID:        card.uid,
				VCardText:  card.text,
				SearchMeta: card.meta,
			}, store.Precondition{}, bookIDs)
			if err != nil {
				return err
			}
			imported++
		}
		return nil
	})
	if err != nil {
		http.Error(w, "import failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	target := "/dashboard/contacts?imported=" + strconv.Itoa(imported) +
		"&lists=" + strconv.Itoa(created) + "&skipped=" + strconv.Itoa(skipped)
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// parseImportFile returns the cards in a vCard file or a zip archive, plus the
// number of entries it could not parse.
func parseImportFile(data []byte) ([]importCard, int, error) {
	if !looksLikeZip(data) {
		cards, skipped := parseVCards(data)
		if len(cards) == 0 && skipped == 0 {
			return nil, 0, errors.New("no vCards found in the file")
		}
		return cards, skipped, nil
	}

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, 0, errors.New("the zip archive could not be read")
	}
	cards := make([]importCard, 0)
	skipped := 0
	seenVCF := false
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || !strings.EqualFold(filepath.Ext(f.Name), ".vcf") {
			continue
		}
		if strings.HasPrefix(filepath.Base(f.Name), "._") || strings.Contains(f.Name, "__MACOSX/") {
			continue
		}
		seenVCF = true
		rc, err := f.Open()
		if err != nil {
			skipped++
			continue
		}
		entry, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			skipped++
			continue
		}
		got, bad := parseVCards(entry)
		if len(got) == 0 && bad == 0 {
			bad = 1
		}
		cards = append(cards, got...)
		skipped += bad
	}
	if !seenVCF {
		return nil, 0, errors.New("the zip archive holds no .vcf files")
	}
	return cards, skipped, nil
}

func looksLikeZip(data []byte) bool {
	return len(data) >= 4 && data[0] == 'P' && data[1] == 'K' && data[2] == 3 && data[3] == 4
}

// parseVCards splits a byte stream into VCARD blocks, then validates and
// canonicalizes each block. A malformed block is counted, not fatal.
func parseVCards(data []byte) ([]importCard, int) {
	raw := splitVCards(data)
	cards := make([]importCard, 0, len(raw))
	skipped := 0
	for _, chunk := range raw {
		filename, err := newFilename()
		if err != nil {
			skipped++
			continue
		}
		text, uid, meta, err := vcardmeta.FromRaw(chunk, filename)
		if err != nil {
			skipped++
			continue
		}
		_, categories := vcardmeta.Categories(text)
		cards = append(cards, importCard{
			filename:   filename,
			uid:        uid,
			text:       text,
			meta:       meta,
			categories: categories,
		})
	}
	return cards, skipped
}

// splitVCards returns every BEGIN:VCARD ... END:VCARD block in order. Lines
// outside a block, such as a stray header or blank separators, are ignored.
func splitVCards(data []byte) []string {
	s := strings.TrimPrefix(string(data), "\ufeff")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")

	out := make([]string, 0)
	var block []string
	inCard := false
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		boundary := trimmed == line
		switch {
		case !inCard && boundary && strings.EqualFold(trimmed, "BEGIN:VCARD"):
			inCard = true
			block = []string{line}
		case inCard:
			block = append(block, line)
			if boundary && strings.EqualFold(trimmed, "END:VCARD") {
				out = append(out, strings.Join(block, "\n")+"\n")
				inCard = false
				block = nil
			}
		}
	}
	return out
}

// presetExportName builds the name field's starting value. One contact uses its
// full name, two or three enumerate the names, and anything larger falls back to
// "contacts" or the list name plus "_contacts".
func presetExportName(book *store.Book, names []string) string {
	switch {
	case len(names) == 1:
		return underscoreName(names[0])
	case len(names) >= 2 && len(names) <= 3:
		parts := make([]string, len(names))
		for i, name := range names {
			parts[i] = underscoreName(name)
		}
		return strings.Join(parts, " and ")
	case book != nil:
		return underscoreName(book.DisplayName) + "_contacts"
	default:
		return "contacts"
	}
}

func underscoreName(s string) string {
	s = strings.Join(strings.Fields(s), "_")
	if s == "" {
		return "contact"
	}
	return s
}

// safeExportName keeps a submitted file base safe for Content-Disposition. It
// preserves letters, digits, spaces, dots, hyphens, and underscores, and turns
// every other rune into an underscore.
func safeExportName(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '_', r == '-', r == '.', r == ' ':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "contacts"
	}
	return out
}

func parseIDList(raw []string) ([]int64, error) {
	out := make([]int64, 0, len(raw))
	for _, s := range raw {
		if s == "" {
			continue
		}
		id, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, nil
}

// importNotice turns the import redirect's counters into one line for the
// contacts page.
func importNotice(r *http.Request) string {
	q := r.URL.Query()
	if q.Get("imported") == "" && q.Get("skipped") == "" {
		return ""
	}
	imported, _ := strconv.Atoi(q.Get("imported"))
	lists, _ := strconv.Atoi(q.Get("lists"))
	skipped, _ := strconv.Atoi(q.Get("skipped"))

	parts := []string{"Imported " + countNoun(imported, "contact", "contacts") + "."}
	if lists > 0 {
		parts = append(parts, "Created "+countNoun(lists, "list", "lists")+".")
	}
	if skipped > 0 {
		parts = append(parts, "Skipped "+countNoun(skipped, "entry", "entries")+".")
	}
	return strings.Join(parts, " ")
}

func countNoun(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}
