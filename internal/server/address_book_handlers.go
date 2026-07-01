package server

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"

	"mnemo/internal/auth"
	"mnemo/internal/services"
	"mnemo/internal/vcard"
	"mnemo/internal/views/pages"
)

func (r *Router) handleAddressBooksList(w http.ResponseWriter, req *http.Request) {
	sess := auth.UserFromContext(req.Context())
	if sess == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	books, err := r.AddressBookService.ListForUser(req.Context(), sess.UserID)
	if err != nil {
		http.Error(w, "Failed to load address books", http.StatusInternalServerError)
		return
	}

	RenderPage(w, req, "Address Books", pages.AddressBooksList(books, sess.UserID))
}

func (r *Router) handleAddressBookNew(w http.ResponseWriter, req *http.Request) {
	if req.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Retarget", "#modal-container")
		pages.AddressBookForm("").Render(req.Context(), w)
		return
	}
	RenderPage(w, req, "New Address Book", pages.AddressBookForm(""))
}

func (r *Router) handleAddressBookCreate(w http.ResponseWriter, req *http.Request) {
	sess := auth.UserFromContext(req.Context())
	if sess == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if err := req.ParseForm(); err != nil {
		renderBookFormWithError(w, req, "Invalid form submission")
		return
	}

	name := req.FormValue("display_name")
	if name == "" {
		renderBookFormWithError(w, req, "Name is required")
		return
	}

	_, err := r.AddressBookService.Create(req.Context(), sess.UserID, name)
	if err != nil {
		if errors.Is(err, services.ErrSlugTaken) {
			renderBookFormWithError(w, req, "You already have a book with this name")
			return
		}
		renderBookFormWithError(w, req, "Failed to create address book")
		return
	}

	if req.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", "/address-books")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, req, "/address-books", http.StatusSeeOther)
}

func (r *Router) handleAddressBookDelete(w http.ResponseWriter, req *http.Request) {
	sess := auth.UserFromContext(req.Context())
	if sess == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	bookID := chi.URLParam(req, "id")

	if err := r.AddressBookService.Delete(req.Context(), bookID, sess.UserID); err != nil {
		http.Error(w, "Address book not found", http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (r *Router) handleContactsList(w http.ResponseWriter, req *http.Request) {
	bookID := chi.URLParam(req, "id")

	book, err := r.AddressBookService.GetByID(req.Context(), bookID)
	if err != nil {
		http.Error(w, "Address book not found", http.StatusNotFound)
		return
	}

	contacts, err := r.ContactService.ListByBook(req.Context(), bookID)
	if err != nil {
		http.Error(w, "Failed to load contacts", http.StatusInternalServerError)
		return
	}

	RenderPage(w, req, book.DisplayName, pages.ContactsList(contacts, bookID, book.DisplayName))
}

func (r *Router) handleContactNew(w http.ResponseWriter, req *http.Request) {
	bookID := chi.URLParam(req, "id")

	book, err := r.AddressBookService.GetByID(req.Context(), bookID)
	if err != nil {
		http.Error(w, "Address book not found", http.StatusNotFound)
		return
	}

	if req.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Retarget", "#modal-container")
		content := pages.ContactFormFields(bookID, "")
		pages.Modal("New Contact", pages.ModalOpts{ShowClose: true, SizeClass: "max-w-xl"}).Render(templ.WithChildren(req.Context(), content), w)
		return
	}
	RenderPage(w, req, "New Contact", pages.ContactNew(bookID, book.DisplayName, ""))
}

func (r *Router) handleContactCreate(w http.ResponseWriter, req *http.Request) {
	bookID := chi.URLParam(req, "id")

	book, err := r.AddressBookService.GetByID(req.Context(), bookID)
	if err != nil {
		http.Error(w, "Address book not found", http.StatusNotFound)
		return
	}

	if err := req.ParseForm(); err != nil {
		RenderPage(w, req, "New Contact", pages.ContactNew(bookID, book.DisplayName, "Invalid form submission"))
		return
	}

	fd := readContactForm(req)

	if fd.DisplayName == "" {
		RenderPage(w, req, "New Contact", pages.ContactNew(bookID, book.DisplayName, "Name is required"))
		return
	}

	vc := vcard.FromForm(fd)
	vcardText := vc.String()

	_, err = r.ContactService.Create(req.Context(), bookID, fd.DisplayName, vcardText)
	if err != nil {
		RenderPage(w, req, "New Contact", pages.ContactNew(bookID, book.DisplayName, "Failed to create contact"))
		return
	}

	http.Redirect(w, req, "/address-books/"+bookID+"/contacts", http.StatusSeeOther)
}

func (r *Router) handleContactView(w http.ResponseWriter, req *http.Request) {
	bookID := chi.URLParam(req, "id")
	contactID := chi.URLParam(req, "contactId")

	contact, err := r.ContactService.GetByID(req.Context(), contactID)
	if err != nil {
		http.Error(w, "Contact not found", http.StatusNotFound)
		return
	}

	book, err := r.AddressBookService.GetByID(req.Context(), bookID)
	if err != nil {
		http.Error(w, "Address book not found", http.StatusNotFound)
		return
	}

	vc := vcard.Parse(contact.VCardText)
	// Wrap raw base64 as data URI if not already wrapped
	if vc.Photo != "" {
		if !strings.HasPrefix(vc.Photo, "data:") {
			pt := vc.PhotoType
			if pt == "" {
				pt = "image/jpeg"
			}
			vc.Photo = "data:" + pt + ";base64," + vc.Photo
		}
	}
	if req.Header.Get("HX-Target") == "modal-container" {
		w.Header().Set("HX-Push-Url", req.URL.RequestURI())
		pageContent := pages.ContactView(vc, contact.ID, contact.ETag, bookID, book.DisplayName)
		pages.Modal("Contact", pages.ModalOpts{ShowClose: true, ShowExpand: true, ExpandURL: req.URL.RequestURI(), SizeClass: "max-w-2xl", AspectRatio: "aspect-[10/16]", MaxHeight: "max-h-[min(80vh,960px)]"}).Render(templ.WithChildren(req.Context(), pageContent), w)
		return
	}
	RenderPage(w, req, "Contact", pages.ContactView(vc, contact.ID, contact.ETag, bookID, book.DisplayName))
}

func (r *Router) handleContactEdit(w http.ResponseWriter, req *http.Request) {
	bookID := chi.URLParam(req, "id")
	contactID := chi.URLParam(req, "contactId")

	contact, err := r.ContactService.GetByID(req.Context(), contactID)
	if err != nil {
		http.Error(w, "Contact not found", http.StatusNotFound)
		return
	}

	book, err := r.AddressBookService.GetByID(req.Context(), bookID)
	if err != nil {
		http.Error(w, "Address book not found", http.StatusNotFound)
		return
	}

	vc := vcard.Parse(contact.VCardText)
	if req.Header.Get("HX-Target") == "modal-container" {
		w.Header().Set("HX-Push-Url", req.URL.RequestURI())
		content := pages.ContactEdit(vc, contact.ID, contact.ETag, bookID, book.DisplayName, "")
		pages.Modal("Edit Contact", pages.ModalOpts{ShowClose: true, ShowExpand: true, ExpandURL: req.URL.RequestURI(), SizeClass: "max-w-2xl", AspectRatio: "aspect-[10/16]", MaxHeight: "max-h-[min(80vh,960px)]"}).Render(templ.WithChildren(req.Context(), content), w)
		return
	}
	RenderPage(w, req, "Edit Contact", pages.ContactEdit(vc, contact.ID, contact.ETag, bookID, book.DisplayName, ""))
}

func (r *Router) handleContactUpdate(w http.ResponseWriter, req *http.Request) {
	bookID := chi.URLParam(req, "id")
	contactID := chi.URLParam(req, "contactId")

	contact, err := r.ContactService.GetByID(req.Context(), contactID)
	if err != nil {
		http.Error(w, "Contact not found", http.StatusNotFound)
		return
	}

	book, err := r.AddressBookService.GetByID(req.Context(), bookID)
	if err != nil {
		http.Error(w, "Address book not found", http.StatusNotFound)
		return
	}

	vc := vcard.Parse(contact.VCardText)

	if err := req.ParseForm(); err != nil {
		RenderPage(w, req, "Edit Contact", pages.ContactEdit(vc, contactID, contact.ETag, bookID, book.DisplayName, "Invalid form submission"))
		return
	}

	fd := readContactForm(req)

	if fd.DisplayName == "" {
		RenderPage(w, req, "Edit Contact", pages.ContactEdit(vc, contactID, contact.ETag, bookID, book.DisplayName, "Name is required"))
		return
	}

	vc = vcard.FromForm(fd)
	vcardText := vc.String()
	_, err = r.ContactService.UpdateContact(req.Context(), contactID, fd.DisplayName, vcardText)
	if err != nil {
		RenderPage(w, req, "Edit Contact", pages.ContactEdit(vc, contactID, contact.ETag, bookID, book.DisplayName, "Failed to save contact"))
		return
	}

	http.Redirect(w, req, "/address-books/"+bookID+"/contacts", http.StatusSeeOther)
}

func (r *Router) handleContactDelete(w http.ResponseWriter, req *http.Request) {
	contactID := chi.URLParam(req, "contactId")

	if err := r.ContactService.SoftDelete(req.Context(), contactID); err != nil {
		http.Error(w, "Contact not found", http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (r *Router) handleContactsExport(w http.ResponseWriter, req *http.Request) {
	bookID := chi.URLParam(req, "id")
	book, err := r.AddressBookService.GetByID(req.Context(), bookID)
	if err != nil {
		http.Error(w, "Address book not found", http.StatusNotFound)
		return
	}
	contacts, err := r.ContactService.ListByBook(req.Context(), bookID)
	if err != nil {
		http.Error(w, "Failed to load contacts", http.StatusInternalServerError)
		return
	}

	filename := strings.ReplaceAll(book.DisplayName, " ", "_") + ".vcf"
	w.Header().Set("Content-Type", "text/vcard; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	for _, c := range contacts {
		w.Write([]byte(c.VCardText))
		w.Write([]byte("\n"))
	}
}

func (r *Router) handleContactsImportForm(w http.ResponseWriter, req *http.Request) {
	bookID := chi.URLParam(req, "id")
	if req.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Retarget", "#modal-container")
		pages.ImportForm(bookID, "").Render(req.Context(), w)
		return
	}
	RenderPage(w, req, "Import Contacts", pages.ImportForm(bookID, ""))
}

func (r *Router) handleContactsImport(w http.ResponseWriter, req *http.Request) {
	bookID := chi.URLParam(req, "id")

	if err := req.ParseMultipartForm(10 << 20); err != nil {
		http.Error(w, "Invalid upload", http.StatusBadRequest)
		return
	}

	file, _, err := req.FormFile("vcf_file")
	if err != nil {
		http.Error(w, "No file uploaded", http.StatusBadRequest)
		return
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "Failed to read file", http.StatusBadRequest)
		return
	}

	// Strip BOM
	if len(data) > 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		data = data[3:]
	}

	// Undo double-/triple-encoding from phone VCF exports (UTF-8 → Latin-1 byte reinterpretation)
	rawStr := string(data)
	for round := 0; round < 5; round++ {
		runes := []rune(rawStr)
		allLatin1 := true
		for _, r := range runes {
			if r > 255 {
				allLatin1 = false
				break
			}
		}
		if !allLatin1 {
			break
		}
		bs := make([]byte, len(runes))
		for i, r := range runes {
			bs[i] = byte(r)
		}
		rawStr = string(bs)
	}

	content := rawStr

	option := req.FormValue("option")
	dedupMode := req.FormValue("dedup")
	newBookName := req.FormValue("new_book_name")
	sess := auth.UserFromContext(req.Context())

	// Parse manually — vcard.ParseMany has encoding bugs
	type rawEntry struct {
		displayName string
		rawText     string
	}
	var entries []rawEntry
	blocks := strings.Split(content, "BEGIN:VCARD")
	for _, b := range blocks {
		b = strings.TrimSpace(b)
		if b == "" {
			continue
		}
		idx := strings.Index(b, "END:VCARD")
		if idx < 0 {
			continue
		}
		block := b[:idx]

		// Find FN: line
		fn := ""
		for _, line := range strings.Split(block, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "FN:") {
				fn = strings.TrimSpace(line[3:])
				break
			}
		}
		if fn == "" {
			continue
		}

		entries = append(entries, rawEntry{
			displayName: fn,
			rawText:     "BEGIN:VCARD\n" + block + "\nEND:VCARD",
		})
	}

	// DEBUG
	log.Printf("DEBUG IMPORT: found %d entry(s) in uploaded file", len(entries))
	for i, e := range entries {
		log.Printf("DEBUG IMPORT:   entry[%d]: displayName=%q hex=%x", i, e.displayName, []byte(e.displayName))
	}

	var targetBookID string

	switch option {
	case "new":
		name := newBookName
		if name == "" {
			name = "Imported"
		}
		b, err := r.AddressBookService.Create(req.Context(), sess.UserID, name)
		if err != nil {
			http.Error(w, "Failed to create book", http.StatusInternalServerError)
			return
		}
		targetBookID = b.ID
	case "replace":
		r.ContactService.ReplaceAll(req.Context(), bookID)
		targetBookID = bookID
	default:
		targetBookID = bookID
	}

	imported := 0
	skipped := 0
	for _, entry := range entries {
		if entry.displayName == "" {
			continue
		}

		if dedupMode != "none" {
			existing, _ := r.ContactService.ListByBook(req.Context(), targetBookID)
			dup := false
			for _, e := range existing {
				// Simple name-based dedup
				if strings.EqualFold(entry.displayName, e.DisplayName) {
					switch dedupMode {
					case "exact":
						dup = entry.rawText == e.VCardText
					case "name":
						dup = true
					case "name_phone":
						dup = true
					}
					if dup {
						break
					}
				}
			}
			if dup {
				skipped++
				continue
			}
		}

		_, err := r.ContactService.Create(req.Context(), targetBookID, entry.displayName, entry.rawText)
		if err == nil {
			imported++
		}
	}

	status := fmt.Sprintf("Imported <strong>%d</strong> contact(s)", imported)
	if skipped > 0 {
		status += fmt.Sprintf(", skipped <strong>%d</strong> duplicate(s)", skipped)
	}

	importResult := `<div id="modal-backdrop" class="fixed inset-0 z-40 bg-black/50 transition-opacity duration-200" onclick="window.__closeModal()"></div>
<div id="modal-dialog" class="fixed inset-0 z-50 flex items-center justify-center p-4">
	<div class="w-full max-w-sm border border-border rounded-lg bg-white shadow-xl flex flex-col p-6">
		<div class="flex items-center justify-between mb-4">
			<h2 class="text-xl font-bold">Import Complete</h2>
			<button type="button" class="text-muted-foreground hover:text-foreground text-lg leading-none" onclick="window.__closeModal()">&times;</button>
		</div>
		<div class="p-3 rounded bg-green-50 border border-green-200 text-sm text-green-700">` + status + `</div>
	</div>
</div>`
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(importResult))
}

func renderBookFormWithError(w http.ResponseWriter, req *http.Request, errMsg string) {
	w.Header().Set("HX-Retarget", "#modal-container")
	pages.AddressBookForm(errMsg).Render(req.Context(), w)
}

func readContactForm(r *http.Request) *vcard.FormData {
	fd := &vcard.FormData{
		FirstName:   r.FormValue("first_name"),
		LastName:    r.FormValue("last_name"),
		MiddleName:  r.FormValue("middle_name"),
		Prefix:      r.FormValue("prefix"),
		Suffix:      r.FormValue("suffix"),
		DisplayName: r.FormValue("display_name"),
		Org:         r.FormValue("org"),
		Title:       r.FormValue("title"),
		Note:        r.FormValue("note"),
		PhotoB64:    r.FormValue("photo_b64"),
		PhotoType:   r.FormValue("photo_type"),
	}
	fd.PhoneTypes = r.Form["phone_type[]"]
	fd.PhoneVals = r.Form["phone_value[]"]
	fd.EmailTypes = r.Form["email_type[]"]
	fd.EmailVals = r.Form["email_value[]"]
	fd.ExtraNames = r.Form["extra_name[]"]
	fd.ExtraValues = r.Form["extra_value[]"]
	return fd
}

func extractFN(content string, n int) string {
	idx := 0
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "BEGIN:VCARD") {
			if idx == n {
				// Found our card, now find FN:
				continue
			}
			idx++
			continue
		}
		if idx == n && strings.HasPrefix(line, "FN:") {
			return line[3:] + " hex=" + fmt.Sprintf("%x", []byte(line[3:]))
		}
	}
	return "not found"
}
